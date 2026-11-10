package hub

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/update"
)

// Only the latest install code a manager was given installs it: a new code voids the ones before (a leftover code
// can't take the token from the manager installed with the new one), and a join uses up every code it had.
func TestManagersOnlyTheLatestCodeInstalls(t *testing.T) {
	h, srv, _ := managersHub(t)
	var made struct {
		Manager managerInfo `json:"manager"`
		Code    string      `json:"code"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "工作"}, &made)
	id := made.Manager.ID
	var second struct{ Code string }
	if code := managersDo(t, h, srv, "POST", "/api/v1/managers/"+id+"/code", nil, &second); code != 200 {
		t.Fatalf("second code: %d", code)
	}
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": made.Code}, nil); code != 401 {
		t.Fatalf("the first code after a second was made: %d", code)
	}
	var joined struct{ ID, Token string }
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": second.Code}, &joined); code != 200 || joined.ID != id {
		t.Fatalf("the second code: %d %+v", code, joined)
	}
	_ = dialRawBus(t, srv, joined.Token)

	// Installed: a code made to move it, then another; only the last joins, and once.
	var move1, move2 struct{ Code string }
	managersDo(t, h, srv, "POST", "/api/v1/managers/"+id+"/code", nil, &move1)
	managersDo(t, h, srv, "POST", "/api/v1/managers/"+id+"/code", nil, &move2)
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": move1.Code}, nil); code != 401 {
		t.Fatalf("a voided move code: %d", code)
	}
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": move2.Code}, nil); code != 200 {
		t.Fatalf("the latest move code: %d", code)
	}
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": move2.Code}, nil); code != 401 {
		t.Fatalf("a code joins once: %d", code)
	}
}

// From the internet the join, which hands out a token, is refused over plaintext, like the bus and /i/<code>; and the
// code, which crossed the internet in the clear, is voided: whoever saw it on the way can't trade it over TLS. The
// manager stays in the roster, its code expired, and a new code installs it.
func TestManagerJoinRefusesPublicPlaintext(t *testing.T) {
	h, _, _ := managersHub(t)
	mux := h.routes()
	newCode := func(path string) string {
		t.Helper()
		var made struct {
			Manager managerInfo `json:"manager"`
			Code    string      `json:"code"`
		}
		w := securityRequest(mux, "POST", path, h.cfg.Token, `{"name":"工作"}`, "127.0.0.1:1", false)
		if w.Code != 201 && w.Code != 200 {
			t.Fatalf("%s: %d %s", path, w.Code, w.Body.String())
		}
		_ = json.Unmarshal(w.Body.Bytes(), &made)
		return made.Code
	}
	installing := func(id string) *time.Time {
		t.Helper()
		var list struct{ Managers []managerInfo }
		_ = json.Unmarshal(securityRequest(mux, "GET", "/api/v1/managers", h.cfg.Token, "", "127.0.0.1:1", false).Body.Bytes(), &list)
		for _, m := range list.Managers {
			if m.ID == id {
				return m.Installing
			}
		}
		t.Fatalf("%s left the roster: %+v", id, list.Managers)
		return nil
	}
	code := newCode("/api/v1/managers")
	c, err := h.managerStore().ManagerInstallCodeByHash(context.Background(), hashToken(code))
	if err != nil || c == nil {
		t.Fatal(err)
	}
	id := c.ManagerID
	body := func(code string) string { return `{"code":"` + code + `"}` }
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", body(code), "198.51.100.1:1", false), 403, "tls_required")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", body(code), "198.51.100.1:1", true), 401, "bad_install_code")
	if at := installing(id); at == nil || at.After(time.Now()) {
		t.Fatalf("after a plaintext join its code should show expired: %v", at)
	}

	// The same for GET /i/<code> over plaintext from the internet: the code was in its path.
	code = newCode("/api/v1/managers/" + id + "/code")
	requireCode(t, securityRequest(mux, "GET", "/i/"+code, "", "", "198.51.100.1:1", false), 403, "tls_required")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", body(code), "198.51.100.1:1", true), 401, "bad_install_code")

	// A new code, over TLS from there: it joins. And a code nobody knows over plaintext is just refused.
	code = newCode("/api/v1/managers/" + id + "/code")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", body("mi_0123456789abcdef0123456789abcdef"), "198.51.100.1:1", false), 403, "tls_required")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", body(code), "198.51.100.1:1", true), 200, "")
	// From the LAN plaintext is fine (allowed for computers there), and voids nothing.
	code = newCode("/api/v1/managers/" + id + "/code")
	requireCode(t, securityRequest(mux, "POST", bus.JoinPath, "", body(code), "[fd00::21]:1", false), 200, "")
}

// A public address over plaintext can't make the hub look install codes up without limit: a code that voids nothing
// (unknown, or not one) counts against it, at the join and at /i/<code> alike, as an unknown code over TLS does, and
// past the limit it gets 429 without the store being asked. The store not answering counts nothing and is logged
// once a minute, however many requests.
func TestManagerPlaintextVoidIsRateLimited(t *testing.T) {
	h, _, _ := managersHub(t)
	vs := &voidStore{managerStore: h.managerStore()}
	h.bus.idents = vs
	h.cfg.Security.AuthFailures = 3
	var lines atomic.Int32
	h.logger = log.New(writerFunc(func(p []byte) (int, error) {
		if strings.Contains(string(p), "could not void") {
			lines.Add(1)
		}
		return len(p), nil
	}), "", 0)
	mux := h.routes()
	send := func(i int, code, remote string) *httptest.ResponseRecorder {
		if i%2 == 0 {
			return securityRequest(mux, "POST", bus.JoinPath, "", `{"code":"`+code+`"}`, remote, false)
		}
		return securityRequest(mux, "GET", "/i/"+code, "", "", remote, false)
	}
	unknown := func(i int) string { return fmt.Sprintf("mi_%032x", i) }

	for i := range 3 {
		requireCode(t, send(i, unknown(i), "198.51.100.1:1"), 403, "tls_required")
	}
	for i := 3; i < 10; i++ {
		requireCode(t, send(i, unknown(i), "198.51.100.1:1"), 429, "rate_limited")
	}
	if n := vs.calls.Load(); n != 3 {
		t.Fatalf("the store was asked %d times for one address past its limit", n)
	}
	// Not codes at all: counted, and never asked of the store.
	for i := range 3 {
		requireCode(t, send(i, "mi_nope", "198.51.100.2:1"), 403, "tls_required")
	}
	requireCode(t, send(0, unknown(0), "198.51.100.2:1"), 429, "rate_limited")
	if n := vs.calls.Load(); n != 3 {
		t.Fatalf("the store was asked %d times", n)
	}

	// The store down: 80 requests from one address, none counted (still 403, not 429), one log line.
	vs.err = errors.New("the store did not answer")
	for i := range 80 {
		requireCode(t, send(i, unknown(i), "198.51.100.3:1"), 403, "tls_required")
	}
	if n := lines.Load(); n != 1 {
		t.Fatalf("%d log lines for 80 requests with the store down", n)
	}
}

// voidStore is the store with ExpireManagerInstallCode counted, and failing with err while it is set.
type voidStore struct {
	managerStore
	calls atomic.Int32
	err   error
}

func (v *voidStore) ExpireManagerInstallCode(ctx context.Context, hash string) (bool, error) {
	v.calls.Add(1)
	if v.err != nil {
		return false, v.err
	}
	return v.managerStore.ExpireManagerInstallCode(ctx, hash)
}

// The commands name this hub as computers reach it: an https public_url when it is set (with the TLS pin on its own
// TLS port), and through a member hub (whose Host is the mesh's "peer") the hub's own endpoints.
func TestManagerCommandsUseTheHubsOwnAddress(t *testing.T) {
	h, srv, _ := managersHub(t)
	h.public.mu.Lock()
	h.public.port, h.public.fp = 18662, "cGlu"
	h.public.endpoints = []Endpoint{{URL: "http://[fd00::20]:18660", Scheme: "http", Port: 18660},
		{URL: "https://[fd00::20]:18662", Scheme: "https", Port: 18662}}
	h.public.mu.Unlock()
	h.cfg.PublicURL = "https://hub.example:18662"
	var made struct {
		Commands map[string]string `json:"commands"`
		Code     string            `json:"code"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "工作"}, &made)
	if s := made.Commands["service"]; !strings.Contains(s, "https://hub.example:18662/i/"+made.Code) || !strings.Contains(s, "--pinnedpubkey") {
		t.Fatalf("with public_url: %q", s)
	}
	if j := made.Commands["join"]; !strings.Contains(j, "https://hub.example:18662 ") || !strings.Contains(j, "--fingerprint cGlu") {
		t.Fatalf("join with public_url: %q", j)
	}

	h.cfg.PublicURL = ""
	if base, fp := h.managerAddress(viaMember()); base != "https://[fd00::20]:18662" || fp != "cGlu" {
		t.Fatalf("through a member: %q %q", base, fp)
	}
	direct := httptest.NewRequest("POST", "http://[fd00::21]:18660/api/v1/managers", nil)
	if base, _ := h.managerAddress(direct); base != "http://[fd00::21]:18660" {
		t.Fatalf("straight from the app: %q", base)
	}
}

// viaMember is an app's request a member hub forwarded: its Host is the mesh's.
func viaMember() *http.Request {
	r := httptest.NewRequest("POST", "http://peer/api/v1/managers", nil)
	return r.WithContext(context.WithValue(r.Context(), forwardedKey{}, forwardedSource{}))
}

// With the public_url every hub writes for itself (http://<host name>:18660, for the private network only): a
// request over the TLS port gets that port and its pin, a plaintext one where it came, and one through a member the
// hub's pinned https endpoint, the http public_url only when there is nothing else. A public_url with a subpath (a
// reverse proxy, https) keeps it.
func TestManagerAddressWithTheDefaultPublicURL(t *testing.T) {
	h, srv, _ := managersHub(t)
	h.cfg.PublicURL = defaultConfig().PublicURL
	if !strings.HasPrefix(h.cfg.PublicURL, "http://") || !strings.HasSuffix(h.cfg.PublicURL, ":18660") {
		t.Fatalf("the default public_url: %q", h.cfg.PublicURL)
	}
	setEndpoints := func(port int, eps ...Endpoint) {
		h.public.mu.Lock()
		h.public.port, h.public.fp, h.public.endpoints = port, "cGlu", eps
		h.public.mu.Unlock()
	}
	// As collectEndpoints orders them: the host name (public_url) before the LAN address.
	named := Endpoint{URL: h.cfg.PublicURL, Kind: "name", Scheme: "http", Port: 18660}
	lan := Endpoint{URL: "https://[fd00::20]:18662", Kind: "lan", Scheme: "https", Port: 18662}
	setEndpoints(18662, named, lan)

	overTLS := httptest.NewRequest("POST", "https://[fd00::20]:18662/api/v1/managers", nil)
	if overTLS.TLS == nil {
		t.Fatal("no TLS on the request")
	}
	if base, fp := h.managerAddress(overTLS); base != "https://[fd00::20]:18662" || fp != "cGlu" {
		t.Fatalf("over the TLS port: %q %q", base, fp)
	}
	plain := httptest.NewRequest("POST", "http://[fd00::21]:18660/api/v1/managers", nil)
	if base, fp := h.managerAddress(plain); base != "http://[fd00::21]:18660" || fp != "" {
		t.Fatalf("plaintext: %q %q", base, fp)
	}
	if base, fp := h.managerAddress(viaMember()); base != "https://[fd00::20]:18662" || fp != "cGlu" {
		t.Fatalf("through a member: %q %q", base, fp)
	}
	// The whole commands, over the TLS port.
	var made struct {
		Code     string            `json:"code"`
		Commands map[string]string `json:"commands"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "工作"}, &made)
	cmds := h.managerCommands(overTLS, "m-0a1b2c", made.Code)
	if s := cmds["service"]; !strings.Contains(s, "'https://[fd00::20]:18662/i/"+made.Code+"'") || !strings.Contains(s, "--pinnedpubkey") || strings.Contains(s, "18660") {
		t.Fatalf("service over the TLS port: %q", s)
	}
	// Without TLS, through a member: the http public_url, the one address there is.
	setEndpoints(0, named)
	if base, fp := h.managerAddress(viaMember()); base != h.cfg.PublicURL || fp != "" {
		t.Fatalf("through a member, no TLS: %q %q", base, fp)
	}

	// An https public_url under a subpath (a reverse proxy): kept, for straight requests and through a member when
	// the hub has no TLS port of its own.
	h.cfg.PublicURL = "https://hub.example/persona/"
	for name, r := range map[string]*http.Request{"plaintext": plain, "over TLS": overTLS, "through a member": viaMember()} {
		if base, fp := h.managerAddress(r); base != "https://hub.example/persona" || fp != "" {
			t.Errorf("subpath, %s: %q %q", name, base, fp)
		}
	}
	cmds = h.managerCommands(plain, "m-0a1b2c", made.Code)
	if !strings.Contains(cmds["service"], "https://hub.example/persona/i/"+made.Code) || !strings.Contains(cmds["join"], "https://hub.example/persona ") {
		t.Fatalf("subpath commands: %+v", cmds)
	}
	// Through a member with a TLS port too: its pinned endpoint first, then the public_url.
	setEndpoints(18662, lan)
	if base, fp := h.managerAddress(viaMember()); base != "https://[fd00::20]:18662" || fp != "cGlu" {
		t.Fatalf("through a member, TLS and a subpath: %q %q", base, fp)
	}
}

// Calling off a manager still being installed takes its identity and codes in one statement, so a join that commits
// meanwhile leaves no manager behind and its token opens nothing.
func TestManagersCallOffWinsOverAJoin(t *testing.T) {
	h, srv, _ := managersHub(t)
	var made struct {
		Manager managerInfo `json:"manager"`
		Code    string      `json:"code"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "临时"}, &made)
	// The join lands between DELETE's look at the roster (installing) and its delete.
	id, _, tok, err := h.redeemInstallCode(context.Background(), made.Code)
	if err != nil || id != made.Manager.ID {
		t.Fatal(err)
	}
	if _, err := h.revokeBusToken(context.Background(), id); err != nil { // what DELETE does for an installing one now
		t.Fatal(err)
	}
	if b, err := h.busStore().BusIdentityByHash(context.Background(), hashToken(tok)); err != nil || b != nil {
		t.Fatal("the joined token survived the call-off")
	}
	var list managersReply
	managersDo(t, h, srv, "GET", "/api/v1/managers", nil, &list)
	if _, ok := list.find(id); ok {
		t.Fatalf("still listed: %+v", list)
	}
}

// Names: control characters (C0 and C1) and bidirectional formatting characters are refused.
func TestManagerNameRefusesControlAndBidi(t *testing.T) {
	for _, bad := range []string{"a\x00b", "a\u0085b", "a‮b", "a⁦b", "a‏b", "a؜b"} {
		if _, err := managerName(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if n, err := managerName(" 工作 Home "); err != nil || n != "工作 Home" {
		t.Fatalf("%q %v", n, err)
	}
}

// An install code in a path never reaches the log.
func TestLoggedPathMasksInstallCodes(t *testing.T) {
	for in, want := range map[string]string{"/i/mi_0123": "/i/<code>", "/i/mi_0123/docker": "/i/<code>/docker",
		"/api/v1/managers": "/api/v1/managers"} {
		if got := loggedPath(in); got != want {
			t.Errorf("%s: %s", in, got)
		}
	}
}

// Nor does it through the line about a gzipped answer cut off.
func TestGzipAbortLogMasksInstallCodes(t *testing.T) {
	var mu sync.Mutex
	var logs []string
	logf := func(format string, args ...any) {
		mu.Lock()
		defer mu.Unlock()
		logs = append(logs, fmt.Sprintf(format, args...))
	}
	srv := httptest.NewServer(gzipResponses(logf, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/x-shellscript; charset=utf-8")
		w.Header().Set("Content-Length", "2048")
		_, _ = io.WriteString(w, strings.Repeat("echo cut off\n", 120)) // past gzipMinSize, short of 2048
	})))
	defer srv.Close()
	code := "mi_" + strings.Repeat("5ec7", 8)
	if resp, err := srv.Client().Get(srv.URL + "/i/" + code); err == nil {
		_, _ = io.ReadAll(resp.Body)
		resp.Body.Close()
	}
	mu.Lock()
	defer mu.Unlock()
	if len(logs) != 1 || !strings.Contains(logs[0], "/i/<code>") || strings.Contains(logs[0], code) {
		t.Fatalf("logs: %q", logs)
	}
}

// slowSetting answers Setting after a gate opens, so a test can change the roster while a read is under way.
type slowSetting struct {
	managerStore
	gate    chan struct{}
	fail    atomic.Bool
	reading chan struct{}
}

func (s *slowSetting) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == managerDefaultKey {
		if s.reading != nil {
			close(s.reading)
			s.reading = nil
		}
		if s.gate != nil {
			<-s.gate
		}
		if s.fail.Load() {
			return false, errStoreDown
		}
	}
	return s.managerStore.Setting(ctx, key, out)
}

var errStoreDown = errors.New("connection refused")

// The default manager's cache keeps no value a read got from before the roster changed, nor a fallback that was
// only the store failing.
func TestDefaultOwnerCache(t *testing.T) {
	h, _, store := managersHub(t)
	busToken(t, h, "m-abc123", "家")
	if err := store.PutSetting(context.Background(), managerDefaultKey, "m-abc123"); err != nil {
		t.Fatal(err)
	}
	slow := &slowSetting{managerStore: store, gate: make(chan struct{}), reading: make(chan struct{})}
	h.bus.idents = slow
	got := make(chan string, 1)
	reading := slow.reading
	go func() { got <- h.defaultOwner(context.Background()) }()
	<-reading
	h.ownersChanged() // the roster changed while it read
	close(slow.gate)
	if def := <-got; def != "m-abc123" {
		t.Fatalf("read %q", def)
	}
	h.owners.mu.Lock()
	kept := h.owners.def
	h.owners.mu.Unlock()
	if kept != "" {
		t.Fatalf("kept %q from a read that began before the change", kept)
	}

	slow.gate = nil
	slow.fail.Store(true)
	if def := h.defaultOwner(context.Background()); def != "assistant" {
		t.Fatalf("with the store down: %q", def)
	}
	h.owners.mu.Lock()
	kept = h.owners.def
	h.owners.mu.Unlock()
	if kept != "" {
		t.Fatalf("kept the fallback %q of a failed read", kept)
	}
}

// expireOwners ages what ownerCache keeps (the default, the identities) past defaultOwnerTTL.
func (h *Hub) expireOwners() {
	h.owners.mu.Lock()
	h.owners.until, h.owners.identsUntil = time.Time{}, time.Time{}
	h.owners.mu.Unlock()
}

// rosterSettings counts the default's reads besides the roster's.
type rosterSettings struct {
	*rosterReads
	settings atomic.Int32
}

func (r *rosterSettings) Setting(ctx context.Context, key string, out any) (bool, error) {
	if key == managerDefaultKey {
		r.settings.Add(1)
	}
	return r.DB.Setting(ctx, key, out)
}

// The roster's identities and the default are read once for the lists of a moment; a token the hub makes or revokes
// shows at once, and a roster that cannot be read once the moment passed still fails what needs it.
func TestRosterKeptAMoment(t *testing.T) {
	h, srv, store := newBusHub(t)
	busToken(t, h, "m-0b0b0b", "B")
	reads := &rosterSettings{rosterReads: &rosterReads{DB: store}}
	h.bus.idents = reads
	managers := func() (int, []string) {
		t.Helper()
		req, _ := http.NewRequest("GET", srv.URL+"/api/v1/managers", nil)
		req.Header.Set("Authorization", "Bearer "+h.cfg.Token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out struct{ Managers []managerInfo }
		_ = json.NewDecoder(resp.Body).Decode(&out)
		var ids []string
		for _, m := range out.Managers {
			ids = append(ids, m.ID)
		}
		return resp.StatusCode, ids
	}
	reads.lists.Store(0)
	for range 5 {
		if code, ids := managers(); code != 200 || !slices.Contains(ids, "m-0b0b0b") {
			t.Fatalf("%d %v", code, ids)
		}
		if ids, err := h.retainedListManagers(t.Context()); err != nil || !slices.Contains(ids, "m-0b0b0b") {
			t.Fatalf("%v %v", ids, err)
		}
	}
	if l, s := reads.lists.Load(), reads.settings.Load(); l != 1 || s != 1 {
		t.Fatalf("10 lists read the identities %d times and the default %d times, want once each", l, s)
	}

	busToken(t, h, "m-0c0c0c", "C")
	if _, ids := managers(); !slices.Contains(ids, "m-0c0c0c") {
		t.Fatalf("a new token: %v", ids)
	}
	if _, err := h.revokeBusToken(t.Context(), "m-0b0b0b"); err != nil {
		t.Fatal(err)
	}
	if _, ids := managers(); slices.Contains(ids, "m-0b0b0b") {
		t.Fatalf("a revoked token: %v", ids)
	}
	if ids, _ := h.retainedListManagers(t.Context()); slices.Contains(ids, "m-0b0b0b") {
		t.Fatalf("a revoked token in the kept lists: %v", ids)
	}

	reads.away.Store(true)
	h.expireOwners()
	if code, _ := managers(); code != http.StatusServiceUnavailable {
		t.Fatalf("the roster unreadable: %d", code)
	}
	if _, err := h.retainedListManagers(t.Context()); err == nil {
		t.Fatal("the roster unreadable, yet the kept lists' managers")
	}
	reads.away.Store(false)
	if code, ids := managers(); code != 200 || !slices.Contains(ids, "m-0c0c0c") {
		t.Fatalf("back: %d %v", code, ids)
	}
}

// The install script checks the download against the sha256 the hub puts in it, from the release it read: not a
// SHA256SUMS fetched from the same place as the download (that only when the hub knows none).
func TestManagerInstallScriptCarriesChecksums(t *testing.T) {
	h := newTestHub(t)
	if h.upd == nil {
		h.upd = newUpdater(h)
	}
	sum := strings.Repeat("ab", 32)
	h.upd.mu.Lock()
	h.upd.release = &update.Published{Manifest: &update.Manifest{Version: "1.2.3", Files: map[string]update.File{
		"linux-amd64": {Name: "personad-linux-amd64.gz", SHA256: sum}, "bad": {Name: "x';rm -rf /;'", SHA256: sum}}}}
	h.upd.mu.Unlock()
	version, sums := h.releaseSums()
	script := managerInstallScript(managerScript{ID: "m-0a1b2c", Name: "工作", Hub: "https://[fd00::20]:18662", Code: "mi_x",
		DL: "https://dl.example/dl", Version: version, Sums: sums, HubVersion: "1.2.2"})
	if !strings.Contains(script, "SUMS='"+sum+"  personad-linux-amd64.gz\n'") || strings.Contains(script, "x';") ||
		!strings.Contains(script, "installing the published 1.2.3") {
		t.Fatalf("script:\n%s", script)
	}
	plain := managerInstallScript(managerScript{ID: "m-0a1b2c", Hub: "http://[fd00::20]:18660", Code: "mi_x", DL: "https://dl.example/dl"})
	if !strings.Contains(plain, "SUMS=''") || !strings.Contains(plain, `fetch "$DL/SHA256SUMS"`) {
		t.Fatalf("without checksums:\n%s", plain)
	}
	f := t.TempDir() + "/i.sh"
	if err := os.WriteFile(f, []byte(script), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("sh", "-n", f).CombinedOutput(); err != nil {
		t.Fatalf("sh -n: %v %s", err, out)
	}
}

// joinInFlight runs a join's statements (db.RedeemManagerInstallCode) for code in a transaction of its own and
// leaves it open: the code's row is locked and the identity saved, not committed. commit ends it.
func joinInFlight(t *testing.T, code string) (commit func()) {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, os.Getenv("PERSONA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var id, name string
	if err := tx.QueryRow(ctx, `UPDATE manager_install_codes SET used_at = now()
		WHERE code_hash = $1 AND used_at IS NULL AND expires_at > now() RETURNING manager_id, name`,
		hashToken(code)).Scan(&id, &name); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `UPDATE manager_install_codes SET used_at = now() WHERE manager_id = $1 AND used_at IS NULL`, id); err != nil {
		t.Fatal(err)
	}
	var had string
	if err := tx.QueryRow(ctx, `SELECT name FROM bus_identities WHERE id = $1 FOR UPDATE`, id).Scan(&had); err != nil && !errors.Is(err, pgx.ErrNoRows) {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `INSERT INTO bus_identities (id, role, name, token_hash) VALUES ($1, 'manager', $2, $3)
		ON CONFLICT (id) DO UPDATE SET role = 'manager', name = $2, token_hash = $3, created_at = now()`,
		id, name, hashToken(busTokenPrefix+randomHex(24))); err != nil {
		t.Fatal(err)
	}
	return func() {
		if err := tx.Commit(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
}

// revokeDuringJoin revokes manager id while a join for code is in flight: the revoke waits on the code's row, the
// join commits, and the manager it saved goes too.
func revokeDuringJoin(t *testing.T, h *Hub, store *db.DB, id, code string) {
	t.Helper()
	commit := joinInFlight(t, code)
	type result struct {
		ok  bool
		err error
	}
	done := make(chan result, 1)
	go func() {
		ok, err := h.revokeBusToken(context.Background(), id)
		done <- result{ok, err}
	}()
	// The revoke is blocked on the row lock the join holds.
	conn, err := pgx.Connect(context.Background(), os.Getenv("PERSONA_TEST_DATABASE_URL"))
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(context.Background())
	deadline := time.Now().Add(10 * time.Second)
	for {
		var waiting int
		if err := conn.QueryRow(context.Background(), `SELECT count(*) FROM pg_stat_activity
			WHERE datname = current_database() AND wait_event_type = 'Lock'
			  AND query LIKE '%DELETE FROM manager_install_codes%'`).Scan(&waiting); err != nil {
			t.Fatal(err)
		}
		if waiting > 0 {
			break
		}
		select {
		case r := <-done:
			t.Fatalf("the revoke did not wait for the join: %+v", r)
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("the revoke never waited on the join's row lock")
		}
		time.Sleep(20 * time.Millisecond)
	}
	select {
	case r := <-done:
		t.Fatalf("the revoke finished while the join held the code: %+v", r)
	case <-time.After(100 * time.Millisecond):
	}
	commit()
	var r result
	select {
	case r = <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the revoke never finished after the join committed")
	}
	if r.err != nil || !r.ok {
		t.Fatalf("revoke: %v %v", r.ok, r.err)
	}
	if b, err := store.BusIdentityByID(context.Background(), id); b != nil || err != nil {
		t.Fatalf("the join's manager stayed after the revoke: %+v %v", b, err)
	}
	if c, err := store.ManagerInstallCodeByHash(context.Background(), hashToken(code)); c != nil || err != nil {
		t.Fatalf("its code stayed: %+v %v", c, err)
	}
}

// A revoke that runs while a join is in flight removes what the join installs, for a new manager and for one
// installed already that is moving.
func TestManagerRevokeWaitsForAJoinInFlight(t *testing.T) {
	h, srv, store := managersHub(t)
	var made struct {
		Manager managerInfo `json:"manager"`
		Code    string      `json:"code"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "工作"}, &made)
	revokeDuringJoin(t, h, store, made.Manager.ID, made.Code)

	// Installed, then a code to move it: the move's join is in flight when it is revoked.
	var other struct {
		Manager managerInfo `json:"manager"`
		Code    string      `json:"code"`
	}
	managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "家"}, &other)
	var joined struct{ ID, Token string }
	if code := managersDo(t, h, srv, "POST", bus.JoinPath, map[string]any{"code": other.Code}, &joined); code != 200 {
		t.Fatalf("join: %d", code)
	}
	var move struct{ Code string }
	if code := managersDo(t, h, srv, "POST", "/api/v1/managers/"+joined.ID+"/code", nil, &move); code != 200 {
		t.Fatalf("move code: %d", code)
	}
	revokeDuringJoin(t, h, store, joined.ID, move.Code)
}

// An install script carries the sums of the release on the site now: when the hub read latest.json a minute ago or
// more, /i/<code> reads it again (the download is the new one), at most once a minute; when that fails the old sums
// stay. Only a read that worked counts: a periodic check that failed does not make the old release fresh.
func TestManagerInstallScriptReadsTheReleaseAgain(t *testing.T) {
	h, srv, _ := managersHub(t)
	p := newPublisher(t, "v1.2.4", 202611060900)
	var reads atomic.Int32
	p.asked = func(path string) {
		if path == "/dl/latest.json" {
			reads.Add(1)
		}
	}
	h.build = update.Make("v1.2.3-202611021500", update.Release, "202611021500", p.srv.URL+"/dl")
	h.upd.client = p.srv.Client()
	name := "personad-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
	old := strings.Repeat("ab", 32)
	setRelease := func(read time.Time) {
		h.upd.mu.Lock()
		h.upd.release = &update.Published{Manifest: &update.Manifest{Version: "v1.2.3", Build: 202611021500,
			Files: map[string]update.File{runtime.GOOS + "-" + runtime.GOARCH: {Name: name, SHA256: old}}}}
		h.upd.lastCheck, h.upd.releaseRead, h.upd.refreshTried = read, read, time.Time{}
		h.upd.mu.Unlock()
	}
	script := func() string {
		t.Helper()
		var made struct{ Code string }
		managersDo(t, h, srv, "POST", "/api/v1/managers", map[string]any{"name": "工作"}, &made)
		resp, err := http.Get(srv.URL + "/i/" + made.Code)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		if resp.StatusCode != 200 {
			t.Fatalf("/i/: %d %s", resp.StatusCode, b)
		}
		return string(b)
	}
	// The sha256 of the bytes the site serves for this computer now.
	resp, err := http.Get(p.srv.URL + "/dl/" + name)
	if err != nil {
		t.Fatal(err)
	}
	served, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	sum := sha256.Sum256(served)
	newer := hex.EncodeToString(sum[:])
	reads.Store(0)

	fresh := func(what string) {
		t.Helper()
		if s := script(); !strings.Contains(s, newer+"  "+name) || strings.Contains(s, old) || !strings.Contains(s, "v1.2.4") {
			t.Fatalf("%s: a stale release's sums:\n%s", what, s)
		}
	}
	// Read 6 hours ago: the script has the new release's sums, read just now.
	setRelease(time.Now().Add(-6 * time.Hour))
	fresh("read 6 hours ago")
	if n := reads.Load(); n != 1 {
		t.Fatalf("latest.json read %d times", n)
	}
	// Read 2 minutes ago (the release came out since): read again too.
	setRelease(time.Now().Add(-2 * time.Minute))
	fresh("read 2 minutes ago")
	// Read 30 seconds ago: the hub's release is used as it is.
	reads.Store(0)
	setRelease(time.Now().Add(-30 * time.Second))
	if s := script(); !strings.Contains(s, old) || reads.Load() != 0 {
		t.Fatalf("read again though fresh (%d):\n%s", reads.Load(), s)
	}

	// The site fails: the sums from before stay, and the next request within a minute does not try again.
	served200 := p.srv.Config.Handler
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		http.Error(w, "down", http.StatusBadGateway)
	})
	reads.Store(0)
	setRelease(time.Now().Add(-time.Hour))
	for range 3 {
		if s := script(); !strings.Contains(s, old+"  "+name) {
			t.Fatalf("lost the sums it had:\n%s", s)
		}
	}
	if n := reads.Load(); n != 1 {
		t.Fatalf("tried latest.json %d times within a minute", n)
	}
	if s := script(); !strings.Contains(s, "A release may have come out since the hub last checked") {
		t.Fatalf("the mismatch message:\n%s", s)
	}
	// A minute after that try, with the site back: read again.
	p.srv.Config.Handler = served200
	h.upd.mu.Lock()
	h.upd.refreshTried = time.Now().Add(-releaseRefreshEvery - time.Second)
	h.upd.mu.Unlock()
	fresh("a minute after a failed read")

	// The periodic check failing just now does not make the release from hours ago fresh.
	setRelease(time.Now().Add(-6 * time.Hour))
	p.srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "down", http.StatusBadGateway)
	})
	if _, err := h.upd.check(context.Background()); err == nil {
		t.Fatal("the check worked against a site that is down")
	}
	p.srv.Config.Handler = served200
	fresh("after a failed periodic check")
}
