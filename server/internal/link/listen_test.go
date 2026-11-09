package link

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/netaddr"
)

// The tests here play the hub as plan-hub-center.md section 4 has it: pin the connector's key from the pairing link,
// trade the code for a credential, and open streams with that credential to a fake resident node. Everything listens
// on 127.0.0.1 and a port the system picks, in temporary directories.

// fakeNode is a resident node: it echoes what a stream sends, after a greeting of its own.
type fakeNode struct {
	path    string
	attachs atomic.Int32
}

const nodeHello = "node says hello\n"

func newFakeNode(t *testing.T) *fakeNode {
	t.Helper()
	dir, err := os.MkdirTemp("", "jl") // short: a unix socket path has a length limit
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	n := &fakeNode{path: filepath.Join(dir, "node.sock")}
	ln, err := net.Listen("unix", n.path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.attachs.Add(1)
			go func() {
				defer c.Close()
				if _, err := c.Write([]byte(nodeHello)); err != nil {
					return
				}
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return n
}

// connector runs persona-link (Run) on a link.json of its own in passive mode, listening on 127.0.0.1.
type connector struct {
	path   string
	addr   string // host:port it listens on
	status chan Status
	last   atomic.Pointer[Status]
}

func startConnector(t *testing.T, target string, o Options) *connector {
	t.Helper()
	c := &connector{path: filepath.Join(t.TempDir(), "link.json"), status: make(chan Status, 1024)}
	if _, err := SetMode(c.path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	c.run(t, target, o)
	s := c.waitFor(t, func(s Status) bool { return s.Mode == ModePassive && s.State == "unpaired" && s.Listen != "" })
	c.addr = s.Listen
	return c
}

func (c *connector) run(t *testing.T, target string, o Options) {
	o.Path, o.Target, o.Version = c.path, target, "test-version"
	o.Status = func(s Status) {
		c.last.Store(&s)
		select {
		case c.status <- s:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = Run(ctx, o) }()
	t.Cleanup(func() { cancel(); <-done })
}

func (c *connector) waitFor(t *testing.T, ok func(Status) bool) Status {
	t.Helper()
	if s := c.last.Load(); s != nil && ok(*s) {
		return *s
	}
	timeout := time.After(10 * time.Second)
	for {
		select {
		case s := <-c.status:
			if ok(s) {
				return s
			}
		case <-timeout:
			last := c.last.Load()
			t.Fatalf("no such status; the last was %+v", last)
		}
	}
}

// invite is `persona-link pair --listen --addr <where it listens>`.
func (c *connector) invite(t *testing.T) (fp, code string) {
	t.Helper()
	inv, err := PairListen(c.path, "", []string{c.addr}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	addrs, fp, code, err := ParseConnectorLink(inv.Link)
	if err != nil || len(addrs) != 1 || addrs[0] != c.addr || fp != inv.FP {
		t.Fatalf("link %s: %v %v", inv.Link, addrs, err)
	}
	return fp, code
}

// hubSide is the hub's side: a client pinned to the connector's key.
type hubSide struct {
	t      *testing.T
	base   string
	client *http.Client
}

func hubFor(t *testing.T, addr, fp string) *hubSide {
	h := &hubSide{t: t, base: "https://" + addr, client: pinned(fp, nil)}
	t.Cleanup(h.client.CloseIdleConnections)
	return h
}

type reply struct {
	status int
	body   map[string]string
	header http.Header
}

func (h *hubSide) do(method, path, token string, body any) reply {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.base+path, rd)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	out := reply{status: resp.StatusCode, header: resp.Header}
	_ = json.NewDecoder(resp.Body).Decode(&out.body)
	return out
}

func (h *hubSide) pair(code, token string) reply {
	return h.do(http.MethodPost, ConnectorPairPath, "", map[string]string{"code": code, "token": token, "machine": "example-workstation",
		"hub_id": "0f1e2d3c-0000-4000-8000-000000000000", "hub_name": "家里"})
}

func (h *hubSide) stream(token string) (*websocket.Conn, *http.Response, error) {
	return h.streamWith(bearer(token))
}

// streamWith opens a stream with header: the credential, and what else the hub says.
func (h *hubSide) streamWith(header http.Header) (*websocket.Conn, *http.Response, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "wss"+strings.TrimPrefix(h.base, "https")+ConnectorStreamPath, &websocket.DialOptions{
		HTTPClient: h.client, HTTPHeader: header, CompressionMode: websocket.CompressionContextTakeover})
}

var tokenA = "jd_" + strings.Repeat("a", 40)
var tokenB = "jd_" + strings.Repeat("b", 40)

func TestPassivePairsAndStreamsToTheNode(t *testing.T) {
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{})
	fp, code := c.invite(t)
	if want, _ := Identity(TLSDir(c.path)); fp != want {
		t.Fatalf("link pin %s, identity %s", fp, want)
	}
	// A hub that pinned another key does not get to talk to it.
	if resp, err := pinned("another-key", nil).Get("https://" + c.addr + ConnectorInfoPath); err == nil || !errors.Is(err, errPin) {
		if resp != nil {
			resp.Body.Close()
		}
		t.Fatalf("another pin: %v", err)
	}
	h := hubFor(t, c.addr, fp)
	c.waitFor(t, func(s Status) bool { return s.State == "unpaired" && s.CodeExpires != nil })

	if r := h.pair("jc_wrong", tokenA); r.status != http.StatusUnauthorized || r.body["error"] != "bad_code" {
		t.Fatalf("wrong code: %+v", r)
	}
	for _, bad := range []map[string]string{{"code": code, "token": "short", "machine": "example-workstation"}, {"code": code, "token": tokenA}, {"token": tokenA, "machine": "example-workstation"}} {
		if r := h.do(http.MethodPost, ConnectorPairPath, "", bad); r.status != http.StatusBadRequest {
			t.Fatalf("pairing with %v: %+v", bad, r)
		}
	}
	if r := h.pair(code, tokenA); r.status != http.StatusOK || r.body["version"] != "test-version" {
		t.Fatalf("pair: %+v", r)
	}
	if r := h.pair(code, tokenB); r.status != http.StatusUnauthorized {
		t.Fatalf("the code twice: %+v", r)
	}
	s := c.waitFor(t, func(s Status) bool { return s.State == "listening" })
	if s.Machine != "example-workstation" || s.HubName != "家里" || s.CodeExpires != nil || s.From != "127.0.0.1" || s.LastSeen == nil {
		t.Fatalf("status %+v", s)
	}

	// link.json keeps the credential's hash only, 0600; the identity is 0700 / 0600.
	raw, _ := os.ReadFile(c.path)
	cfg, _ := Read(c.path)
	if strings.Contains(string(raw), tokenA) || cfg.Passive.TokenSHA256 != hashHex(tokenA) || cfg.Passive.HubID == "" || cfg.Passive.Code != nil ||
		cfg.Mode != ModePassive {
		t.Fatalf("link.json %s", raw)
	}
	for p, want := range map[string]os.FileMode{c.path: 0o600, TLSDir(c.path): 0o700 | os.ModeDir,
		filepath.Join(TLSDir(c.path), "link.key"): 0o600, filepath.Join(TLSDir(c.path), "link.crt"): 0o600} {
		if fi, err := os.Stat(p); err != nil || fi.Mode() != want {
			t.Errorf("%s: %v %v, want %v", p, fi.Mode(), err, want)
		}
	}

	if r := h.do(http.MethodGet, ConnectorInfoPath, "", nil); r.status != http.StatusUnauthorized {
		t.Fatalf("info without a credential: %+v", r)
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenB, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("info with another credential: %+v", r)
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenA, nil); r.status != http.StatusOK || r.body["machine"] != "example-workstation" || r.body["version"] != "test-version" {
		t.Fatalf("info: %+v", r)
	}
	// Nothing else is there, with a credential or without.
	for _, req := range [][2]string{{"GET", "/"}, {"GET", "/api/v1/connector"}, {"GET", ConnectorInfoPath + "/"}, {"POST", ConnectorInfoPath},
		{"GET", ConnectorPairPath}, {"PUT", ConnectorStreamPath}, {"GET", "/api/v1/link"}, {"GET", "/../etc/passwd"}} {
		if r := h.do(req[0], req[1], tokenA, nil); r.status != http.StatusNotFound {
			t.Errorf("%s %s: %d", req[0], req[1], r.status)
		}
	}
	if r := h.do(http.MethodGet, ConnectorStreamPath, tokenA, nil); r.status != http.StatusBadRequest {
		t.Fatalf("a stream without the upgrade: %+v", r)
	}

	// The stream: the node's bytes, both ways, as they are.
	ws, resp, err := h.stream(tokenA)
	if err != nil {
		t.Fatalf("stream: %v %v", err, resp)
	}
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer nc.Close()
	hello := make([]byte, len(nodeHello))
	if _, err := io.ReadFull(nc, hello); err != nil || string(hello) != nodeHello {
		t.Fatalf("node hello %q %v", hello, err)
	}
	s = c.waitFor(t, func(s Status) bool { return s.State == "connected" })
	if s.Address != "127.0.0.1" || s.Node != "online" || s.Since == nil {
		t.Fatalf("connected status %+v", s)
	}
	payload := make([]byte, 300<<10) // every byte value, past any buffer size
	_, _ = rand.Read(payload)
	go func() { _, _ = nc.Write(payload) }()
	back := make([]byte, len(payload))
	if _, err := io.ReadFull(nc, back); err != nil || !bytes.Equal(back, payload) {
		t.Fatalf("echo: %v, same %v", err, bytes.Equal(back, payload))
	}
	// The hub ends the stream: the node sees EOF and goes, and the connector is back to listening.
	nc.Close()
	c.waitFor(t, func(s Status) bool { return s.State == "listening" })
	if node.attachs.Load() != 1 {
		t.Fatalf("%d node connections", node.attachs.Load())
	}
}

func TestPassiveRateLimitsFailedAuthentication(t *testing.T) {
	node := newFakeNode(t)
	lim := newAuthLimiter()
	lim.failures, lim.block = 3, time.Minute
	c := startConnector(t, node.path, Options{limiter: lim})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair %+v", r)
	}
	for i := range 3 {
		if r := h.do(http.MethodGet, ConnectorInfoPath, tokenB, nil); r.status != http.StatusUnauthorized {
			t.Fatalf("failure %d: %+v", i+1, r)
		}
	}
	// Blocked now: even the right credential waits, and says how long.
	for _, token := range []string{tokenB, tokenA} {
		r := h.do(http.MethodGet, ConnectorInfoPath, token, nil)
		if retry, _ := strconv.Atoi(r.header.Get("Retry-After")); r.status != http.StatusTooManyRequests || r.body["error"] != "rate_limited" || retry < 1 || retry > 60 {
			t.Fatalf("blocked: %+v", r)
		}
	}
	if _, resp, err := h.stream(tokenA); err == nil || resp == nil || resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("stream while blocked: %v %v", err, resp)
	}
	// Wrong pairing codes count the same way.
	lim2 := newAuthLimiter()
	lim2.failures = 2
	c2 := startConnector(t, node.path, Options{limiter: lim2})
	fp2, code2 := c2.invite(t)
	h2 := hubFor(t, c2.addr, fp2)
	h2.pair("jc_x", tokenA)
	h2.pair("jc_y", tokenA)
	if r := h2.pair(code2, tokenA); r.status != http.StatusTooManyRequests {
		t.Fatalf("the right code from a blocked source: %+v", r)
	}
}

func TestPassiveCodeRunsOut(t *testing.T) {
	c := startConnector(t, newFakeNode(t).path, Options{})
	fp, code := c.invite(t)
	if err := Update(c.path, func(cfg *Config) error {
		cfg.Passive.Code.Expires = time.Now().Add(-time.Second)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if r := hubFor(t, c.addr, fp).pair(code, tokenA); r.status != http.StatusUnauthorized || r.body["error"] != "bad_code" {
		t.Fatalf("expired code: %+v", r)
	}
	if cfg, _ := Read(c.path); cfg.Passive.Paired() {
		t.Fatal("paired with an expired code")
	}
}

func TestPassivePairingAgainEndsTheOldCredential(t *testing.T) {
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair %+v", r)
	}
	ws, _, err := h.stream(tokenA)
	if err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() {
		for {
			if _, _, err := ws.Read(context.Background()); err != nil {
				ended <- err
				return
			}
		}
	}()
	c.waitFor(t, func(s Status) bool { return s.State == "connected" })
	// Another hub pairs (the center changed): the first one's stream ends with 4002 and its credential is no good.
	_, code2 := c.invite(t)
	if r := h.pair(code2, tokenB); r.status != http.StatusOK {
		t.Fatalf("pair again %+v", r)
	}
	select {
	case err := <-ended:
		if websocket.CloseStatus(err) != CloseRevoked {
			t.Fatalf("old stream ended with %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("old stream still open")
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenA, nil); r.status != http.StatusUnauthorized {
		t.Fatalf("old credential: %+v", r)
	}
	if _, resp, err := h.stream(tokenA); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("old credential's stream: %v", err)
	}
	if r := h.do(http.MethodGet, ConnectorInfoPath, tokenB, nil); r.status != http.StatusOK {
		t.Fatalf("new credential: %+v", r)
	}
}

func TestPassiveWithoutTheNode(t *testing.T) {
	target := filepath.Join(t.TempDir(), "no-node.sock")
	c := startConnector(t, target, Options{})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	h.pair(code, tokenA)
	_, resp, err := h.stream(tokenA)
	if err == nil || resp == nil || resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("%v %v", err, resp)
	}
	// The same advice as active mode: how this OS runs the node, not the bare command (#3723). The hub shows the
	// answer's message on the machine's page; the menu bar shows node_error.
	want := nodeMissing(runtime.GOOS, target).Error()
	var body struct{ Error, Message string }
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil || body.Error != "node_unavailable" || body.Message != want {
		t.Fatalf("answer %+v (%v), want message %q", body, err, want)
	}
	s := c.waitFor(t, func(s Status) bool { return s.Node == "offline" })
	if s.State != "listening" || s.NodeError != want {
		t.Fatalf("status %+v, want node_error %q", s, want)
	}
}

func TestPassiveStreamNoticesAHubThatStoppedAnswering(t *testing.T) {
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond})
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	h.pair(code, tokenA)
	ws, _, err := h.stream(tokenA) // and never read again: no pongs
	if err != nil {
		t.Fatal(err)
	}
	defer ws.CloseNow()
	c.waitFor(t, func(s Status) bool { return s.State == "connected" })
	c.waitFor(t, func(s Status) bool { return s.State == "listening" })
}

// A stream follows the heartbeat the hub says in PingHeader (hub.json link), either way; without one, or with one that
// is not two numbers, the connector keeps its own.
func TestPassiveStreamFollowsTheHubsHeartbeat(t *testing.T) {
	open := func(t *testing.T, o Options, ping string) *connector {
		t.Helper()
		node := newFakeNode(t)
		c := startConnector(t, node.path, o)
		fp, code := c.invite(t)
		h := hubFor(t, c.addr, fp)
		h.pair(code, tokenA)
		header := bearer(tokenA)
		if ping != "" {
			header.Set(PingHeader, ping)
		}
		ws, _, err := h.streamWith(header) // and never read again: no pongs
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { ws.CloseNow() })
		c.waitFor(t, func(s Status) bool { return s.State == "connected" })
		return c
	}
	for _, tc := range []struct {
		name, ping string
		o          Options
	}{
		{"shorter", PingHeaderValue(50*time.Millisecond, 50*time.Millisecond), Options{}}, // 10 s and 5 s: 15 s
		{"none", "", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}},
		{"not numbers", "soon later", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}},
		{"half", "60000", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := open(t, tc.o, tc.ping)
			// The latest status, not the queue: it holds statuses from before the stream.
			eventually(t, func() bool { return c.last.Load().State == "listening" }) // within 5 s
		})
	}
	t.Run("longer", func(t *testing.T) {
		c := open(t, Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond},
			PingHeaderValue(time.Minute, time.Minute))
		for until := time.Now().Add(time.Second); time.Now().Before(until); time.Sleep(10 * time.Millisecond) {
			if s := c.last.Load(); s.State != "connected" {
				t.Fatalf("dropped despite the hub's longer heartbeat: %+v", s)
			}
		}
	})
}

// A request whose body stalls is closed after passiveReadTimeout, whatever the path (pairing reads the body before any
// code is checked; net/http drains a small unread body before it answers), while a stream outlives that deadline.
func TestPassiveClosesARequestWhoseBodyStalls(t *testing.T) {
	was := passiveReadTimeout
	passiveReadTimeout = 300 * time.Millisecond
	t.Cleanup(func() { passiveReadTimeout = was }) // after the connector's cleanup has stopped it
	node := newFakeNode(t)
	c := startConnector(t, node.path, Options{})

	// The stream is opened first: the stalled requests below each take passiveReadTimeout to be closed, so once they
	// are, the stream's own request deadline has passed too.
	fp, code := c.invite(t)
	h := hubFor(t, c.addr, fp)
	if r := h.pair(code, tokenA); r.status != http.StatusOK {
		t.Fatalf("pair: %+v", r)
	}
	ws, resp, err := h.stream(tokenA)
	if err != nil {
		t.Fatalf("stream: %v %v", err, resp)
	}
	nc := websocket.NetConn(context.Background(), ws, websocket.MessageBinary)
	defer nc.Close()
	hello := make([]byte, len(nodeHello))
	if _, err := io.ReadFull(nc, hello); err != nil {
		t.Fatalf("node hello: %v", err)
	}

	for _, head := range []string{"POST " + ConnectorPairPath, "GET /elsewhere"} {
		conn, err := tls.Dial("tcp", c.addr, &tls.Config{InsecureSkipVerify: true, NextProtos: []string{"http/1.1"}})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.WriteString(conn, head+" HTTP/1.1\r\nHost: x\r\nContent-Type: application/json\r\nContent-Length: 4000\r\n\r\n{\"code\":")
		_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
		got, err := io.ReadAll(conn)
		conn.Close()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			t.Fatalf("%s: the listener kept the connection of a stalled body open (read %q)", head, got)
		}
		if len(got) > 0 && !strings.HasPrefix(string(got), "HTTP/1.1 4") { // 400 from pairing, 404 elsewhere, or just closed
			t.Fatalf("%s: %q", head, got)
		}
	}

	if _, err := nc.Write([]byte("still here")); err != nil {
		t.Fatalf("write after the deadline: %v", err)
	}
	back := make([]byte, len("still here"))
	if _, err := io.ReadFull(nc, back); err != nil || string(back) != "still here" {
		t.Fatalf("echo after the deadline: %q %v", back, err)
	}
}

func TestModeSwitchesBothWays(t *testing.T) {
	node := newFakeNode(t)
	c := &connector{path: filepath.Join(t.TempDir(), "link.json"), status: make(chan Status, 1024)}
	c.run(t, node.path, Options{})
	c.waitFor(t, func(s Status) bool { return s.Mode == ModeActive && s.State == "unpaired" })
	if _, err := SetMode(c.path, ModePassive, "127.0.0.1:0"); err != nil {
		t.Fatal(err)
	}
	s := c.waitFor(t, func(s Status) bool { return s.Mode == ModePassive && s.Listen != "" })
	addr := s.Listen
	if conn, err := net.Dial("tcp", addr); err != nil {
		t.Fatalf("not listening: %v", err)
	} else {
		conn.Close()
	}
	if _, err := SetMode(c.path, ModeActive, ""); err != nil {
		t.Fatal(err)
	}
	c.waitFor(t, func(s Status) bool { return s.Mode == ModeActive && s.State == "unpaired" })
	eventually(t, func() bool {
		conn, err := net.Dial("tcp", addr)
		if err == nil {
			conn.Close()
		}
		return err != nil
	})
	cfg, _ := Read(c.path)
	if cfg.Passive == nil || cfg.Passive.Listen != "127.0.0.1:0" {
		t.Fatalf("passive mode's settings stay for switching back: %+v", cfg)
	}
}

func TestPassiveListenFailsWhenThePortIsTaken(t *testing.T) {
	taken, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer taken.Close()
	c := &connector{path: filepath.Join(t.TempDir(), "link.json"), status: make(chan Status, 1024)}
	if _, err := SetMode(c.path, ModePassive, taken.Addr().String()); err != nil {
		t.Fatal(err)
	}
	c.run(t, filepath.Join(t.TempDir(), "x.sock"), Options{})
	s := c.waitFor(t, func(s Status) bool { return s.State == "waiting" })
	if !strings.Contains(s.Error, "端口被占用") || s.Retry == nil {
		t.Fatalf("status %+v", s)
	}
}

func TestActivePairingKeepsPassiveMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if err := Update(path, func(c *Config) error {
		c.Mode, c.Passive = ModePassive, &Passive{Listen: ":18663", TokenSHA256: hashHex(tokenA), Machine: "example-workstation"}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); !errors.Is(err, ErrUnpaired) || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("only passive: %v", err)
	}
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"token": "jd_link", "role": "link"})
	}))
	defer hub.Close()
	u, _ := netip.ParseAddrPort(strings.TrimPrefix(hub.URL, "https://"))
	link := fmt.Sprintf("persona://link?url=http%%3A%%2F%%2F127.0.0.1%%3A1&tls=%d&token=jp_x&hub=h1&fp=%s&machine=laptop", u.Port(), pinOf(hub))
	if _, err := Pair(context.Background(), link, "", "", path); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil || c.CurrentMode() != ModeActive || c.Token != "jd_link" || c.Passive == nil || c.Passive.TokenSHA256 != hashHex(tokenA) {
		t.Fatalf("%+v %v", c, err)
	}
}

func TestIdentityKeepsItsKey(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "link-tls")
	now := time.Now()
	id, err := loadIdentity(dir, now)
	if err != nil {
		t.Fatal(err)
	}
	first := id.crt.Leaf
	if len(first.DNSNames) != 1 || first.DNSNames[0] != tlsName || len(first.IPAddresses) != 0 || first.NotAfter.Before(now.Add(300*24*time.Hour)) {
		t.Fatalf("certificate %v %v %v", first.DNSNames, first.IPAddresses, first.NotAfter)
	}
	again, _ := loadIdentity(dir, now)
	if again.fp != id.fp || !bytes.Equal(again.crt.Leaf.Raw, first.Raw) {
		t.Fatal("loading again changed the identity")
	}
	// Near its end the certificate is renewed with the same key: the pin stays.
	later := first.NotAfter.Add(-time.Hour)
	crt, err := again.certificate(later)
	if err != nil || bytes.Equal(crt.Leaf.Raw, first.Raw) || Pin(crt.Leaf) != id.fp || !crt.Leaf.NotAfter.After(later.Add(300*24*time.Hour)) {
		t.Fatalf("renewed %v", err)
	}
}

func TestListenAddresses(t *testing.T) {
	for raw, want := range map[string]string{"": ":18663", "18663": ":18663", ":1": ":1", "0.0.0.0:18663": "0.0.0.0:18663",
		"[::]:18663": "[::]:18663", "127.0.0.1:0": "127.0.0.1:0", "[fd00::1]:9": "[fd00::1]:9"} {
		if got, err := NormalizeListen(raw); err != nil || got != want {
			t.Errorf("NormalizeListen(%q) = %q %v, want %q", raw, got, err, want)
		}
	}
	for _, bad := range []string{"host.example.com:1", ":99999", "x", "[fe80::1%en0]:1"} {
		if _, err := NormalizeListen(bad); err == nil {
			t.Errorf("NormalizeListen(%q) passed", bad)
		}
	}
	for raw, want := range map[string]string{"hub.example.com": "hub.example.com:18663", "2001:db8::5": "[2001:db8::5]:18663",
		"[2001:db8::5]:1": "[2001:db8::5]:1", "192.0.2.10:443": "192.0.2.10:443"} {
		if got, err := normalizeAddr(raw, ":18663"); err != nil || got != want {
			t.Errorf("normalizeAddr(%q) = %q %v, want %q", raw, got, err, want)
		}
	}
	if _, err := normalizeAddr("hub.example.com", "127.0.0.1:0"); err == nil {
		t.Error("no port to take")
	}
}

func TestListenCandidatesFollowActiveModesOrder(t *testing.T) {
	up := net.FlagUp
	stable := func(ip string) netaddr.Addr {
		return netaddr.Addr{IP: netip.MustParseAddr(ip), Interface: "eth0", InterfaceFlags: up, FlagsKnown: true, ScopeKnown: true,
			Flags: netaddr.FlagPermanent}
	}
	addrs := []netaddr.Addr{
		{IP: netip.MustParseAddr("8.8.4.4"), Interface: "eth0", InterfaceFlags: up},
		stable("2400:cb00::10"),
		{IP: netip.MustParseAddr("2400:cb00::99"), Interface: "eth0", InterfaceFlags: up, FlagsKnown: true, ScopeKnown: true, Flags: netaddr.FlagTemporary},
		{IP: netip.MustParseAddr("fd00::10"), Interface: "eth0", InterfaceFlags: up},
		{IP: netip.MustParseAddr("127.0.0.1"), Interface: "lo", InterfaceFlags: up | net.FlagLoopback},
		{IP: netip.MustParseAddr("fe80::1"), Interface: "eth0", InterfaceFlags: up},
		{IP: netip.MustParseAddr("fd00::20"), Interface: "docker0", InterfaceFlags: up, Virtual: true},
		{IP: netip.MustParseAddr("fd00::30"), Interface: "eth1"},                       // down
		{IP: netip.MustParseAddr("192.0.2.10"), Interface: "eth0", InterfaceFlags: up}, // documentation
	}
	old := localAddrs
	localAddrs = func() ([]netaddr.Addr, error) { return addrs, nil }
	defer func() { localAddrs = old }()
	got, err := ListenCandidates(":18663")
	if err != nil || strings.Join(got, " ") != "[fd00::10]:18663 [2400:cb00::10]:18663 8.8.4.4:18663" {
		t.Fatalf("%v %v", got, err)
	}
	if got, _ := ListenCandidates("0.0.0.0:18663"); strings.Join(got, " ") != "8.8.4.4:18663" {
		t.Fatalf("IPv4 only: %v", got)
	}
	if got, _ := ListenCandidates("[fd00::10]:7"); strings.Join(got, " ") != "[fd00::10]:7" {
		t.Fatalf("one address: %v", got)
	}
	// pair --listen without --addr puts them in the link, in that order.
	path := filepath.Join(t.TempDir(), "link.json")
	inv, err := PairListen(path, "", nil, time.Now())
	if err != nil || !strings.HasPrefix(inv.Link, "persona://connector?addr=%5Bfd00%3A%3A10%5D%3A18663&addr=%5B2400%3Acb00%3A%3A10%5D%3A18663&addr=8.8.4.4%3A18663&fp=") {
		t.Fatalf("%+v %v", inv, err)
	}
	localAddrs = func() ([]netaddr.Addr, error) { return addrs[4:6], nil }
	if _, err := PairListen(path, "", nil, time.Now()); !errors.Is(err, ErrNoAddress) || !strings.Contains(err.Error(), "--addr") {
		t.Fatalf("nothing usable: %v", err)
	}
	// Every interface virtual (a container's eth0): those count then, tunnels and bridges still not.
	localAddrs = func() ([]netaddr.Addr, error) {
		return []netaddr.Addr{
			{IP: netip.MustParseAddr("fd00::42"), Interface: "eth0", InterfaceFlags: up, Virtual: true},
			{IP: netip.MustParseAddr("fd00::43"), Interface: "docker0", InterfaceFlags: up, Virtual: true},
			{IP: netip.MustParseAddr("fd00::44"), Interface: "utun3", InterfaceFlags: up, Virtual: true},
		}, nil
	}
	if got, _ := ListenCandidates(":18663"); strings.Join(got, " ") != "[fd00::42]:18663" {
		t.Fatalf("all virtual: %v", got)
	}
}

func TestConnectorLinks(t *testing.T) {
	link := ConnectorLink([]string{"[fd00::10]:18663", "hub.example.com:18663"}, "pin_-x", "jc_abc")
	addrs, fp, code, err := ParseConnectorLink(" " + link + "\n")
	if err != nil || strings.Join(addrs, ",") != "[fd00::10]:18663,hub.example.com:18663" || fp != "pin_-x" || code != "jc_abc" {
		t.Fatalf("%v %v %v %v", addrs, fp, code, err)
	}
	for _, bad := range []string{"persona://link?addr=a:1&fp=x&code=y", "persona://connector?fp=x&code=y", "persona://connector?addr=a:1&code=y",
		"persona://connector?addr=nohost&fp=x&code=y"} {
		if _, _, _, err := ParseConnectorLink(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAuthLimiterMakesRoom(t *testing.T) {
	l := newAuthLimiter()
	l.failures, l.max = 2, 2
	now := time.Now()
	l.fail("a", now)
	l.fail("a", now) // a is blocked
	l.fail("b", now)
	l.fail("c", now) // full: b goes (not blocked), a stays blocked
	if l.retry("a", now) == 0 || len(l.buckets) != 2 {
		t.Fatalf("%v", l.buckets)
	}
	if l.retry("a", now.Add(authBlock)) != 0 {
		t.Fatal("still blocked after the block")
	}
	if sourceKey(netip.MustParseAddr("2400:cb00::1")) != sourceKey(netip.MustParseAddr("2400:cb00::ffff")) ||
		sourceKey(netip.MustParseAddr("::ffff:203.0.113.5")) != "203.0.113.5" {
		t.Fatal("sources")
	}
}
