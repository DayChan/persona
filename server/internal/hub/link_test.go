package hub

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/link"
	"github.com/nwverse/persona/server/internal/rpc"
)

// newLinkHub is a hub with a link machine "laptop" and an ssh machine "box", served over TLS as on the public port,
// and the manager that connects them (Persona Link dials the hub, which relays it to the manager).
func newLinkHub(t *testing.T) (*Hub, *Machine, *httptest.Server) {
	h, _, srv := newLinkHubManaged(t)
	return h, mirror(t, h, "laptop"), srv
}

func newLinkHubManaged(t *testing.T) (*Hub, *testManager, *httptest.Server) {
	t.Helper()
	h := newTestHub(t)
	h.store = newPairStore()
	h.cfg.Machines = []MachineConfig{{ID: "laptop", Name: "笔记本", Kind: "link"}, {ID: "box", Name: "台式机", Kind: "ssh"}}
	srv := httptest.NewTLSServer(h.routes())
	t.Cleanup(srv.Close)
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	h.public.mu.Lock()
	h.public.port, h.public.fp = port, spkiPin(srv.Certificate())
	h.public.endpoints = []Endpoint{{URL: srv.URL, Kind: "lan", Port: port, Scheme: "https"}}
	h.public.mu.Unlock()
	h.cfg.PublicURL = "http://127.0.0.1:" + strconv.Itoa(port)
	mgr := startTestManager(t, h, srv, map[string]func(context.Context) (net.Conn, error){
		"box": func(context.Context) (net.Conn, error) { return nil, errors.New("no ssh in tests") }})
	mirror(t, h, "laptop")
	return h, mgr, srv
}

// linkVersion is the version of the machine's live link as the hub shows it ("" without one).
func linkVersion(m *Machine) string {
	if l := m.snapshot().Link; l != nil {
		return l.Version
	}
	return ""
}

func linkToken(t *testing.T, h *Hub, machine string) (token, id string) {
	t.Helper()
	token, id = "jd_"+randomHex(24), "dev_"+randomHex(6)
	dt := db.DeviceToken{ID: id, Name: "link", Kind: "link", Role: roleLink, MachineID: machine}
	if err := h.store.CreateDeviceToken(context.Background(), dt, hashToken(token)); err != nil {
		t.Fatal(err)
	}
	return token, id
}

// fakeResidentNode listens like `personad node --attach` and answers every call of every connection with its number.
func fakeResidentNode(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "jl") // unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	path := filepath.Join(dir, "attach.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	var n atomic.Int32
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			id := n.Add(1)
			go func() {
				defer c.Close()
				p := rpc.New("n", c)
				p.OnRequest = func(context.Context, string, json.RawMessage) (any, error) {
					return map[string]any{"pid": id, "hostname": "laptop"}, nil
				}
				_ = p.Serve(context.Background(), c)
			}()
		}
	}()
	return path
}

// runLink runs persona-link (internal/link) with cfg; stop ends it.
func runLink(t *testing.T, cfg link.Config, target string) (statuses chan link.Status, path string, stop func()) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "link.json")
	if err := link.Update(path, func(c *link.Config) error { *c = cfg; return nil }); err != nil {
		t.Fatal(err)
	}
	statuses = make(chan link.Status, 256)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = link.Run(ctx, link.Options{Path: path, Target: target, Version: "test-build",
			Status: func(s link.Status) {
				select {
				case statuses <- s:
				default:
				}
			}})
	}()
	var once atomic.Bool
	stop = func() {
		if once.CompareAndSwap(false, true) {
			cancel()
			<-done
		}
	}
	t.Cleanup(stop)
	return statuses, path, stop
}

func linkConfig(h *Hub, srv *httptest.Server, token string) link.Config {
	return link.Config{Hub: "hub", Machine: "laptop", FP: spkiPin(srv.Certificate()), Token: token,
		Endpoints: []link.Endpoint{{URL: srv.URL, Kind: "lan"}}}
}

func eventually(t *testing.T, within time.Duration, what string, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(within)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatalf("not within %s: %s", within, what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func waitStatus(t *testing.T, ch chan link.Status, what string, ok func(link.Status) bool) link.Status {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case s := <-ch:
			if ok(s) {
				return s
			}
		case <-timeout:
			t.Fatalf("persona-link never reported %s", what)
		}
	}
}

func pid(t *testing.T, m *Machine) int {
	t.Helper()
	var out struct {
		PID int `json:"pid"`
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := m.call(ctx, "ping", map[string]any{}, &out); err != nil {
		t.Fatalf("call over the link: %v", err)
	}
	return out.PID
}

func TestLinkMachineComesOnlineThroughPersonaLink(t *testing.T) {
	h, m, srv := newLinkHub(t)
	h.linkPing = [2]time.Duration{200 * time.Millisecond, 2 * time.Second} // the hub's heartbeat on the link
	token, _ := linkToken(t, h, "laptop")
	target := fakeResidentNode(t)
	cfg := linkConfig(h, srv, token)
	cfg.Endpoints = append(cfg.Endpoints, link.Endpoint{URL: "https://hub.example.com:18662", Kind: "name", Manual: true})
	statuses, path, stop := runLink(t, cfg, target)

	eventually(t, 10*time.Second, "machine online", func() bool { return m.snapshot().Online })
	first := pid(t, m)
	st := waitStatus(t, statuses, "the node online", func(s link.Status) bool { return s.State == "connected" && s.Node == "online" })
	if st.Address != srv.URL || st.Kind != "lan" || st.Since == nil {
		t.Fatalf("status %+v", st)
	}
	if l := m.snapshot().Link; l == nil || l.Version != "test-build" || l.Address != srv.URL {
		t.Fatalf("machine list shows link %+v", l)
	}
	// Its round trip is the hub's heartbeat's (devices.link_rtt): the paths are compared by it.
	eventually(t, 5*time.Second, "the link's round trip", func() bool {
		r := m.snapshot().Route
		return r != nil && r.RTTMS != nil && *r.RTTMS > 0
	})
	// The hub's endpoints replaced the ones from pairing; the one added on the laptop stayed.
	eventually(t, 5*time.Second, "endpoints saved", func() bool {
		c, err := link.Load(path)
		return err == nil && len(c.Endpoints) == 2 && c.Endpoints[0].Manual && c.Endpoints[1].URL == srv.URL
	})

	// The link going away takes the machine offline right then, not at the next ssh-style retry.
	stop()
	eventually(t, 3*time.Second, "machine offline", func() bool { return !m.snapshot().Online && m.snapshot().Link == nil })
	if err := m.call(context.Background(), "ping", nil, nil); !errors.Is(err, errOffline) {
		t.Fatalf("call while offline: %v", err)
	}

	statuses, _, _ = runLink(t, cfg, target)
	eventually(t, 10*time.Second, "machine online again", func() bool { return m.snapshot().Online })
	if again := pid(t, m); again == first {
		t.Fatal("still the old node connection")
	}
	waitStatus(t, statuses, "connected again", func(s link.Status) bool { return s.State == "connected" })
}

func TestLinkWithoutResidentNode(t *testing.T) {
	h, m, srv := newLinkHub(t)
	token, _ := linkToken(t, h, "laptop")
	statuses, _, _ := runLink(t, linkConfig(h, srv, token), "/tmp/persona-link-no-such.sock")
	st := waitStatus(t, statuses, "the node missing", func(s link.Status) bool { return s.State == "connected" && s.Node == "offline" })
	if !strings.Contains(st.NodeError, "常驻 node") {
		t.Fatalf("node error %q", st.NodeError)
	}
	eventually(t, 5*time.Second, "the machine says why", func() bool { return strings.Contains(m.snapshot().Error, "常驻 node") })
	if m.snapshot().Online || m.snapshot().Link == nil {
		t.Fatal("the link is up but the node is not")
	}
}

func dialLink(t *testing.T, srv *httptest.Server, token string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "wss"+strings.TrimPrefix(srv.URL, "https")+link.Path,
		&websocket.DialOptions{HTTPClient: srv.Client(), HTTPHeader: http.Header{"Authorization": {"Bearer " + token}}})
}

func hello(t *testing.T, c *websocket.Conn, machine string) {
	t.Helper()
	b, _ := json.Marshal(link.Message{Type: "hello", Machine: machine, Version: "manual"})
	if err := c.Write(context.Background(), websocket.MessageText, b); err != nil {
		t.Fatal(err)
	}
}

// closedWith reads until the hub closes the link and returns its close code.
func closedWith(t *testing.T, c *websocket.Conn) websocket.StatusCode {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for {
		if _, _, err := c.Read(ctx); err != nil {
			return websocket.CloseStatus(err)
		}
	}
}

func TestLinkReplacedAndRevoked(t *testing.T) {
	h, m, srv := newLinkHub(t)
	token, id := linkToken(t, h, "laptop")
	statuses, _, _ := runLink(t, linkConfig(h, srv, token), fakeResidentNode(t))
	eventually(t, 10*time.Second, "machine online", func() bool { return m.snapshot().Online })

	// A second persona-link with the same credential takes over; the first is told so.
	c, _, err := dialLink(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello(t, c, "laptop")
	waitStatus(t, statuses, "being replaced", func(s link.Status) bool {
		return s.State == "waiting" && strings.Contains(s.Error, "又连了一条")
	})
	eventually(t, 3*time.Second, "the new link in place", func() bool { return linkVersion(m) == "manual" })

	h.revoked(id)
	if code := closedWith(t, c); code != link.CloseRevoked {
		t.Fatalf("revoked link closed with %d", code)
	}
	eventually(t, 3*time.Second, "no link", func() bool { return linkVersion(m) == "" })
}

// Deleting a link computer on the admin page ends its Persona Link: the credential and an unused code for it stop
// working and its open link is closed as revoked, so the computer shows it was refused instead of retrying until a
// computer of the same id is added again.
func TestDeletedLinkComputerIsRefused(t *testing.T) {
	h, m, srv := newLinkHub(t)
	if err := os.WriteFile(filepath.Join(h.cfg.dir, "hub.json"),
		[]byte(`{"machines":[{"id":"laptop","name":"Laptop","kind":"link"},{"id":"box","name":"Box","kind":"ssh"}]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	token, _ := linkToken(t, h, "laptop")
	unused, err := h.newPairCode(context.Background(), "laptop")
	if err != nil {
		t.Fatal(err)
	}
	statuses, _, _ := runLink(t, linkConfig(h, srv, token), fakeResidentNode(t))
	eventually(t, 10*time.Second, "machine online", func() bool { return m.snapshot().Online })

	if w := adminMachinesRequest(h, "DELETE", "/api/v1/admin/machines/laptop", ""); w.Code != http.StatusOK {
		t.Fatalf("delete: %d %s", w.Code, w.Body.String())
	}
	waitStatus(t, statuses, "refused", func(s link.Status) bool { return s.State == "refused" })
	if _, ok := h.authenticate(context.Background(), token); ok {
		t.Fatal("the deleted computer's credential still works")
	}
	if _, ok := h.authenticate(context.Background(), unused.Token); ok {
		t.Fatal("an unused code for the deleted computer still works")
	}
}

func TestLinkRefusesWhatItShouldNot(t *testing.T) {
	h, _, srv := newLinkHub(t)
	token, _ := linkToken(t, h, "laptop")
	phone := "jd_" + randomHex(24)
	_ = h.store.CreateDeviceToken(context.Background(), db.DeviceToken{ID: "dev_phone", Name: "iPhone", Kind: "phone", Role: roleFull}, hashToken(phone))
	boxToken, _ := linkToken(t, h, "box")

	for _, tc := range []struct {
		name, token string
		status      int
	}{
		{"a phone's credential", phone, http.StatusForbidden},
		{"a link credential of a machine that is not kind link", boxToken, http.StatusNotFound},
		{"no credential", "", http.StatusUnauthorized},
	} {
		if _, resp, err := dialLink(t, srv, tc.token); err == nil || resp == nil || resp.StatusCode != tc.status {
			t.Errorf("%s: %v %v, want %d", tc.name, err, resp, tc.status)
		}
	}
	// A link credential opens nothing else.
	req, _ := http.NewRequest("GET", srv.URL+"/api/v1/machines", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	if resp, err := srv.Client().Do(req); err != nil || resp.StatusCode != http.StatusForbidden {
		t.Fatalf("machines with a link credential: %v %v", err, resp)
	}
	// Plaintext: refused from the LAN as well; only loopback, or a LAN reverse proxy that took HTTPS, may skip TLS.
	w := securityRequest(h.routes(), "GET", link.Path, token, "", "[fd00::20]:5000", false)
	requireCode(t, w, http.StatusForbidden, "tls_required")
	// A hello for another machine.
	c, _, err := dialLink(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello(t, c, "box")
	if code := closedWith(t, c); code != link.CloseRefused {
		t.Fatalf("hello for another machine closed with %d", code)
	}
}

func TestLinkHeartbeatDropsADeadLink(t *testing.T) {
	h, m, srv := newLinkHub(t)
	h.linkPing = [2]time.Duration{50 * time.Millisecond, 50 * time.Millisecond} // the hub's side of the link
	token, _ := linkToken(t, h, "laptop")
	c, _, err := dialLink(t, srv, token)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	hello(t, c, "laptop") // and never read again: pongs are only sent while reading
	eventually(t, 3*time.Second, "link up", func() bool { return linkVersion(m) != "" })
	eventually(t, 3*time.Second, "dead link dropped", func() bool { return linkVersion(m) == "" })
	// The link is gone once its context is cancelled; the machine says why a moment later (dropLink).
	eventually(t, 3*time.Second, "the machine says why", func() bool { return strings.Contains(m.snapshot().Error, "no pong") })
}

func TestLinkPairing(t *testing.T) {
	h, m, _ := newLinkHub(t)
	code := func(body string) *httptest.ResponseRecorder {
		return securityRequest(h.routes(), "POST", "/api/v1/auth/pair-codes", h.cfg.Token, body, "127.0.0.1:1", false)
	}
	requireCode(t, code(`{"machine":"box"}`), http.StatusBadRequest, "")
	requireCode(t, code(`{"machine":"nope"}`), http.StatusBadRequest, "")
	w := code(`{"machine":"laptop"}`)
	requireCode(t, w, http.StatusOK, "")
	var p pairCode
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if p.Machine != "laptop" || !strings.HasPrefix(p.PairURL, "persona://link?") || !strings.Contains(p.PairURL, "machine=laptop") {
		t.Fatalf("pair code %+v", p)
	}
	// What a code made for the laptop can do: become the laptop's link credential, nothing else.
	requireCode(t, securityRequest(h.routes(), "GET", "/api/v1/machines", p.Token, "", "127.0.0.1:1", false), http.StatusForbidden, "")
	requireCode(t, securityRequest(h.routes(), "POST", "/api/v1/auth/tokens", p.Token, `{"kind":"link","machine":"box"}`, "127.0.0.1:1", false),
		http.StatusForbidden, "")

	path := filepath.Join(t.TempDir(), "link.json")
	c, err := link.Pair(context.Background(), p.PairURL, "", "", path)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := link.Load(path)
	if err != nil || saved.Machine != "laptop" || saved.Token == "" || saved.FP != h.public.fp || len(saved.Endpoints) == 0 || c.Token != saved.Token {
		t.Fatalf("saved %+v, %v", saved, err)
	}
	who, ok := h.authenticate(context.Background(), saved.Token)
	if !ok || who.Role != roleLink || who.Machine != "laptop" {
		t.Fatalf("redeemed credential %+v", who)
	}
	if _, err := link.Pair(context.Background(), p.PairURL, "", "", path); err == nil {
		t.Fatal("a pairing code works twice")
	}
	// The credential it wrote opens the link.
	statuses, _, _ := runLink(t, *saved, fakeResidentNode(t))
	waitStatus(t, statuses, "connected", func(s link.Status) bool { return s.State == "connected" })
	eventually(t, 10*time.Second, "machine online", func() bool { return m.snapshot().Online })

	// The apps' pairing code can make one too, for a link machine named in the request.
	w = code("")
	_ = json.Unmarshal(w.Body.Bytes(), &p)
	if strings.HasPrefix(p.PairURL, "persona://link?") {
		t.Fatal("a code for the apps")
	}
	if _, err := link.Pair(context.Background(), p.PairURL, "box", "", filepath.Join(t.TempDir(), "x.json")); err == nil {
		t.Fatal("a link credential for an ssh machine")
	}
	if _, err := link.Pair(context.Background(), p.PairURL, "laptop", "", filepath.Join(t.TempDir(), "y.json")); err != nil {
		t.Fatal(err)
	}

	// No TLS, no persona-link.
	h.public.mu.Lock()
	h.public.port = 0
	h.public.mu.Unlock()
	requireCode(t, code(`{"machine":"laptop"}`), http.StatusConflict, "")
}

func TestLinkTellsWhatTheHubIsCalled(t *testing.T) {
	h, _, srv := newLinkHub(t)
	h.cfg.Name = "家里"
	h.sendLinkEndpoints() // as the admin page does when the name changes: the manager tells the links
	eventually(t, 5*time.Second, "the manager has the new name", func() bool {
		h.devices.mu.Lock()
		defer h.devices.mu.Unlock()
		return strings.Contains(string(h.devices.sent[devicesOwner]), "家里")
	})
	token, _ := linkToken(t, h, "laptop")
	statuses, path, _ := runLink(t, linkConfig(h, srv, token), fakeResidentNode(t))
	st := waitStatus(t, statuses, "the hub's name", func(s link.Status) bool { return s.State == "connected" && s.HubName != "" })
	if st.HubName != "家里" || st.MeshHubs != 1 || st.Pending {
		t.Fatalf("status %+v", st)
	}
	eventually(t, 5*time.Second, "the name kept in link.json", func() bool {
		c, err := link.Load(path)
		return err == nil && c.HubName == "家里"
	})
	if m := h.linkEndpoints(); m.Type != "endpoints" || m.HubName != "家里" || m.MeshHubs != 1 || len(m.Endpoints) != 1 {
		t.Fatalf("endpoints message %+v", m)
	}
}

// A computer waiting to be confirmed is told so on its link, and told again once confirmed (#4655): Persona Link shows
// "Waiting for you to confirm on your phone" until then. A confirmed one is never told it is pending, and the others' links are not.
func TestLinkTellsItIsPending(t *testing.T) {
	h, _, srv := newLinkHub(t)
	h.setPendingLocal("laptop", time.Now(), true) // as markMachinePending does, without the database
	eventually(t, 5*time.Second, "the manager has the pending computer", func() bool {
		h.devices.mu.Lock()
		defer h.devices.mu.Unlock()
		return strings.Contains(string(h.devices.sent[devicesOwner]), `"pending":["laptop"]`)
	})
	token, _ := linkToken(t, h, "laptop")
	statuses, _, _ := runLink(t, linkConfig(h, srv, token), fakeResidentNode(t))
	waitStatus(t, statuses, "pending", func(s link.Status) bool { return s.State == "connected" && s.Pending })

	h.setPendingLocal("laptop", time.Time{}, false) // confirmed
	st := waitStatus(t, statuses, "confirmed", func(s link.Status) bool { return s.State == "connected" && !s.Pending })
	if b, _ := json.Marshal(st); strings.Contains(string(b), "pending") {
		t.Fatalf("a confirmed computer's status still says pending: %s", b)
	}
	// Another computer pending: this link is not told it is.
	h.setPendingLocal("elsewhere", time.Now(), true)
	eventually(t, 5*time.Second, "the manager has the other pending computer", func() bool {
		h.devices.mu.Lock()
		defer h.devices.mu.Unlock()
		return strings.Contains(string(h.devices.sent[devicesOwner]), `"pending":["elsewhere"]`)
	})
	select {
	case s := <-statuses:
		if s.Pending {
			t.Fatalf("told another computer's pending: %+v", s)
		}
	case <-time.After(500 * time.Millisecond):
	}
}

func TestHubName(t *testing.T) {
	h := newTestHub(t)
	h.public.id = "3f2a9c1b-0000-4000-8000-000000000000"
	host := ""
	h.hostname = func() (string, error) { return host, nil }
	for _, tc := range []struct{ name, host, local, want string }{
		{"", "nas.local", "", "nas"},
		{"", "MacBook-Pro", "", "MacBook-Pro"},
		{"", "a1b2c3d4e5f6", "", "hub-3f2a9c"}, // a Docker container's id
		{"", strings.Repeat("ab", 32), "", "hub-3f2a9c"},
		{"", "", "", "hub-3f2a9c"},
		{"", "localhost", "", "hub-3f2a9c"},
		{"", "nas", "0", "hub-3f2a9c"}, // the Docker image
		{"  家里\n 书房 ", "nas", "0", "家里 书房"},
		{strings.Repeat("名", 80), "nas", "", strings.Repeat("名", 64) + "…"},
	} {
		h.cfg.Name, host = tc.name, tc.host
		t.Setenv("PERSONA_LOCAL_NODE", tc.local)
		if got := h.hubName(); got != tc.want {
			t.Errorf("name %q, host %q, PERSONA_LOCAL_NODE=%q: %q, want %q", tc.name, tc.host, tc.local, got, tc.want)
		}
	}
	if h.meshHubCount() != 1 {
		t.Fatal("a hub alone is a mesh of one")
	}
}

// Right after the hub started its listeners are up before the manager is: a Persona Link that comes then waits for the
// manager a little instead of being turned away.
func TestLinkWaitsForTheManagerWhileTheHubStarts(t *testing.T) {
	h := newTestHub(t)
	h.store = newPairStore()
	h.cfg.Machines = []MachineConfig{{ID: "laptop", Name: "笔记本", Kind: "link"}}
	h.startDevices(context.Background())
	srv := httptest.NewTLSServer(h.routes())
	t.Cleanup(srv.Close)
	token, _ := linkToken(t, h, "laptop")
	type dialed struct {
		c    *websocket.Conn
		resp *http.Response
		err  error
	}
	got := make(chan dialed, 1)
	go func() {
		c, resp, err := dialLink(t, srv, token)
		got <- dialed{c, resp, err}
	}()
	select {
	case d := <-got:
		t.Fatalf("answered before the manager was there: %v %v", d.resp, d.err)
	case <-time.After(300 * time.Millisecond):
	}
	startTestManager(t, h, srv, nil)
	d := <-got
	if d.err != nil {
		t.Fatalf("the link was turned away: %v %v", d.resp, d.err)
	}
	defer d.c.CloseNow()
	hello(t, d.c, "laptop")
	m := mirror(t, h, "laptop")
	eventually(t, 5*time.Second, "the link up at the manager", func() bool { return linkVersion(m) == "manual" })
}
