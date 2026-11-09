package link

import (
	"context"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"math"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

func pinOf(srv *httptest.Server) string {
	sum := sha256.Sum256(srv.Certificate().RawSubjectPublicKeyInfo)
	return base64.RawURLEncoding.EncodeToString(sum[:])
}

func TestRankedTriesTheLANFirst(t *testing.T) {
	got := ranked([]Endpoint{
		{URL: "https://hub.example.com:18662", Kind: "name"},
		{URL: "https://[2001:db8::1]:18662", Kind: "ipv6"},
		{URL: "https://203.0.113.5:18662", Kind: "ipv4"},
		{URL: "https://192.0.2.10:18662", Kind: "lan"},
		{URL: "https://own.example.com:18662", Kind: "name", Manual: true},
		{URL: "https://192.0.2.10:18662", Kind: "lan"},
	})
	var order []string
	for _, e := range got {
		order = append(order, e.URL)
	}
	want := []string{"https://192.0.2.10:18662", "https://[2001:db8::1]:18662", "https://own.example.com:18662",
		"https://hub.example.com:18662", "https://203.0.113.5:18662"}
	if strings.Join(order, " ") != strings.Join(want, " ") {
		t.Fatalf("order %v", order)
	}
	for raw, kind := range map[string]string{"https://[fd00::2]:1": "lan", "https://[fe80::1]:1": "lan", "https://[2400::1]:1": "ipv6",
		"https://8.8.8.8:1": "ipv4", "https://nas.example.com:1": "name", "https://127.0.0.1:1": "lan"} {
		if got := kindOf(raw); got != kind {
			t.Errorf("kindOf(%s) = %s, want %s", raw, got, kind)
		}
	}
}

func TestParsePairLink(t *testing.T) {
	q := url.Values{"url": {"http://[fd00::10]:18660"}, "token": {"jp_x"}, "hub": {"h1"}, "fp": {"pin"}, "tls": {"18662"},
		"alt": {"https://[2400::1]:18662", "https://hub.example.com:18662"}, "machine": {"laptop"}}
	token, c, err := parsePairLink("persona://link?" + q.Encode())
	if err != nil || token != "jp_x" || c.Machine != "laptop" || c.Hub != "h1" || c.FP != "pin" {
		t.Fatalf("%v %+v", err, c)
	}
	var urls []string
	for _, e := range c.Endpoints {
		urls = append(urls, e.Kind+" "+e.URL)
	}
	if strings.Join(urls, ", ") != "lan https://[fd00::10]:18662, ipv6 https://[2400::1]:18662, name https://hub.example.com:18662" {
		t.Fatalf("candidates %v", urls)
	}
	// The apps' link works too; the machine then comes from the command line.
	q.Del("machine")
	if _, c, err = parsePairLink(" persona://pair?" + q.Encode() + "\n"); err != nil || c.Machine != "" {
		t.Fatalf("%v %+v", err, c)
	}
	q.Del("tls")
	if _, _, err = parsePairLink("persona://pair?" + q.Encode()); err == nil || !strings.Contains(err.Error(), "TLS") {
		t.Fatalf("without TLS: %v", err)
	}
	for _, bad := range []string{"https://example.com", "persona://open?x=1", "not a link"} {
		if _, _, err := parsePairLink(bad); err == nil {
			t.Errorf("%q parsed", bad)
		}
	}
}

func TestAddresses(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if _, err := AddAddress(path, "hub.example.com"); err == nil {
		t.Fatal("added before pairing")
	}
	_ = Update(path, func(c *Config) error {
		*c = Config{Machine: "laptop", FP: "pin", Token: "jd_x", Endpoints: []Endpoint{{URL: "https://192.0.2.10:18662", Kind: "lan"}}}
		return nil
	})
	e, err := AddAddress(path, "hub.example.com")
	if err != nil || e.URL != "https://hub.example.com:18662" || e.Kind != "name" || !e.Manual {
		t.Fatalf("%v %+v", err, e)
	}
	for _, bad := range []string{"http://hub.example.com:18660", "ftp://x", "https://hub.example.com:99999"} {
		if _, err := AddAddress(path, bad); err == nil {
			t.Errorf("%q added", bad)
		}
	}
	if err := RemoveAddress(path, "https://hub.example.com:18662"); err != nil {
		t.Fatal(err)
	}
	c, _ := Load(path)
	if len(c.Endpoints) != 1 || c.Endpoints[0].Manual {
		t.Fatalf("after remove %+v", c.Endpoints)
	}
}

func TestBackoffStaysWithinItsBounds(t *testing.T) {
	var b backoff
	for i, ceiling := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second,
		32 * time.Second, time.Minute, time.Minute, time.Minute} {
		if d := b.next(); d < ceiling/2 || d > ceiling {
			t.Fatalf("attempt %d waits %s, want within [%s, %s]", i, d, ceiling/2, ceiling)
		}
	}
}

func TestRaceStartsInTurnAndKeepsTheFirstThatWorks(t *testing.T) {
	eps := []Endpoint{{URL: "https://a", Kind: "lan"}, {URL: "https://b", Kind: "ipv6"}, {URL: "https://c", Kind: "name"}}
	start := time.Now()
	got, ep, err := race(context.Background(), eps, func(ctx context.Context, ep Endpoint) (string, error) {
		if ep.URL == "https://a" { // the LAN address of a network this laptop is not on: hangs
			<-ctx.Done()
			return "", ctx.Err()
		}
		return ep.URL, nil
	}, func(string) {})
	if err != nil || got != "https://b" || ep.URL != "https://b" {
		t.Fatalf("%v %s %+v", err, got, ep)
	}
	if took := time.Since(start); took < stagger || took > stagger+200*time.Millisecond {
		t.Fatalf("took %s: b starts one stagger after a, and a hanging does not hold it up", took)
	}
	// The LAN answers last but answers: the winner is kept, the latecomer closed.
	var dropped atomic.Int32
	got, _, _ = race(context.Background(), eps[:2], func(ctx context.Context, ep Endpoint) (string, error) {
		if ep.URL == "https://a" {
			time.Sleep(stagger + 100*time.Millisecond)
		}
		return ep.URL, nil
	}, func(string) { dropped.Add(1) })
	if got != "https://b" {
		t.Fatalf("winner %s", got)
	}
	eventually(t, func() bool { return dropped.Load() == 1 })
	_, _, err = race(context.Background(), eps[:2], func(ctx context.Context, ep Endpoint) (string, error) {
		return "", &httpError{status: http.StatusUnauthorized}
	}, func(string) {})
	var he *httpError
	if !errors.As(err, &he) || he.status != http.StatusUnauthorized || !strings.Contains(summary(err), "内网 a：hub 回了 HTTP 401") {
		t.Fatalf("joined error %v / %q", err, summary(err))
	}
}

func eventually(t *testing.T, ok func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !ok() {
		if time.Now().After(deadline) {
			t.Fatal("timed out")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestPinnedRefusesAnotherHub(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	resp, err := pinned("not-its-pin", nil).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("talked to a hub with another key")
	}
	if !errors.Is(err, errPin) {
		t.Fatalf("error %v", err)
	}
	if describe(err) != errPin.Error() {
		t.Fatalf("described as %q", describe(err))
	}
	resp, err = pinned(pinOf(srv), nil).Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
}

// fakeHub accepts links; serve runs each one after its hello.
func fakeHub(t *testing.T, serve func(*websocket.Conn)) (*httptest.Server, *atomic.Int32) {
	return fakeHubHello(t, func(c *websocket.Conn, _ Message) { serve(c) })
}

// fakeHubHello is fakeHub whose serve gets the hello too.
func fakeHubHello(t *testing.T, serve func(*websocket.Conn, Message)) (*httptest.Server, *atomic.Int32) {
	var n atomic.Int32
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != Path || r.Header.Get("Authorization") != "Bearer jd_x" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		var hello Message
		if _, b, err := c.Read(r.Context()); err != nil || json.Unmarshal(b, &hello) != nil || hello.Type != "hello" || hello.Machine != "laptop" {
			return
		}
		n.Add(1)
		serve(c, hello)
	}))
	t.Cleanup(srv.Close)
	return srv, &n
}

func runAgainst(t *testing.T, srv *httptest.Server, token string, o Options) chan Status {
	t.Helper()
	if o.Path == "" {
		o.Path = filepath.Join(t.TempDir(), "link.json")
	}
	_ = Update(o.Path, func(c *Config) error {
		*c = Config{Machine: "laptop", FP: pinOf(srv), Token: token, Endpoints: []Endpoint{{URL: srv.URL, Kind: "lan"}}}
		return nil
	})
	ch := make(chan Status, 256)
	o.Status = func(s Status) {
		select {
		case ch <- s:
		default:
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = Run(ctx, o) }()
	t.Cleanup(func() { cancel(); <-done })
	return ch
}

func waitFor(t *testing.T, ch chan Status, ok func(Status) bool) Status {
	t.Helper()
	timeout := time.After(10 * time.Second)
	for {
		select {
		case s := <-ch:
			if ok(s) {
				return s
			}
		case <-timeout:
			t.Fatal("no such status")
		}
	}
}

func TestLinkNoticesAHubThatStoppedAnswering(t *testing.T) {
	srv, _ := fakeHub(t, func(c *websocket.Conn) { time.Sleep(time.Minute) }) // never reads again: no pongs
	ch := runAgainst(t, srv, "jd_x", Options{PingEvery: 50 * time.Millisecond, PingTimeout: 50 * time.Millisecond})
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Kind == "lan" && s.Address == srv.URL })
	s := waitFor(t, ch, func(s Status) bool { return s.State == "waiting" })
	if !strings.Contains(s.Error, "心跳") || s.Retry == nil {
		t.Fatalf("status %+v", s)
	}
}

// A heartbeat a hub sets is brought into range; half of one, or nonsense, is none (the caller keeps its own).
func TestPingTimesKeepsAHubsHeartbeatInRange(t *testing.T) {
	for _, tc := range []struct {
		every, timeout int64
		want           [2]time.Duration
		ok             bool
	}{
		{30000, 90000, [2]time.Duration{30 * time.Second, 90 * time.Second}, true},
		{1, 1, [2]time.Duration{MinPing, MinPing}, true},
		{3600000, math.MaxInt64, [2]time.Duration{MaxPing, MaxPing}, true},
		{0, 5000, [2]time.Duration{}, false},
		{10000, -1, [2]time.Duration{}, false},
		{-10_000_000_000_000, 5000, [2]time.Duration{}, false}, // would wrap round to a positive Duration
		{10000, math.MinInt64, [2]time.Duration{}, false},
	} {
		every, timeout, ok := PingTimesMS(tc.every, tc.timeout)
		if ok != tc.ok || (ok && [2]time.Duration{every, timeout} != tc.want) {
			t.Errorf("%d / %d ms: %s, %s, %v", tc.every, tc.timeout, every, timeout, ok)
		}
	}
	o := Options{PingEvery: time.Hour, PingTimeout: time.Minute}
	for _, m := range []Message{{}, {PingMS: 30000}, {PingMS: -1, PingTimeoutMS: 5000}} {
		if got := o.heartbeatOf(m); got != (heartbeat{time.Hour, time.Minute, false}) || got.check() != checkPing {
			t.Errorf("%+v: %+v, want Options'", m, got)
		}
	}
	got := o.heartbeatOf(Message{PingMS: 20000, PingTimeoutMS: 60000})
	if got != (heartbeat{20 * time.Second, time.Minute, true}) || got.check() != time.Minute {
		t.Errorf("the hub's: %+v, a check waits %s", got, got.check())
	}
	// A network change is still noticed quickly when the hub's pongs may come sooner than checkPing.
	if got := o.heartbeatOf(Message{PingMS: 1000, PingTimeoutMS: 1000}).check(); got != checkPing {
		t.Errorf("a check after a short heartbeat waits %s", got)
	}
}

// The hub's heartbeat (endpoints) replaces this side's on the link, either way, without reconnecting.
func TestLinkFollowsTheHubsHeartbeat(t *testing.T) {
	t.Run("shorter", func(t *testing.T) {
		srv, _ := fakeHub(t, func(c *websocket.Conn) {
			_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"endpoints","ping_ms":50,"ping_timeout_ms":50}`))
			time.Sleep(time.Minute) // never reads again: no pongs
		})
		ch := runAgainst(t, srv, "jd_x", Options{}) // 10 s and 5 s: the link would stay 15 s
		waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
		start := time.Now()
		s := waitFor(t, ch, func(s Status) bool { return s.State == "waiting" })
		if !strings.Contains(s.Error, "心跳") || time.Since(start) > 5*time.Second {
			t.Fatalf("after %s: %+v", time.Since(start), s)
		}
	})
	t.Run("longer", func(t *testing.T) {
		srv, _ := fakeHub(t, func(c *websocket.Conn) {
			_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"endpoints","ping_ms":60000,"ping_timeout_ms":60000}`))
			time.Sleep(time.Minute) // no pongs: a slow link's, as far as this side can tell
		})
		// Options' first ping would be due after a second and give up 50 ms later; the hub's is a minute away.
		ch := runAgainst(t, srv, "jd_x", Options{PingEvery: time.Second, PingTimeout: 50 * time.Millisecond})
		waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
		hold := time.After(2500 * time.Millisecond)
		for {
			select {
			case s := <-ch:
				if s.State != "connected" {
					t.Fatalf("dropped despite the hub's longer heartbeat: %+v", s)
				}
			case <-hold:
				return
			}
		}
	})
}

func TestLinkReportsARefusedCredential(t *testing.T) {
	srv, _ := fakeHub(t, func(*websocket.Conn) {})
	ch := runAgainst(t, srv, "jd_other", Options{})
	s := waitFor(t, ch, func(s Status) bool { return s.State != "connecting" })
	if s.State != "refused" || !strings.Contains(s.Error, "重新配对") {
		t.Fatalf("status %+v", s)
	}
}

func TestRedialConnectsAgainAtOnce(t *testing.T) {
	srv, n := fakeHub(t, func(c *websocket.Conn) {
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	})
	redial := make(chan struct{}, 1)
	ch := runAgainst(t, srv, "jd_x", Options{Redial: redial})
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	asked := time.Now()
	redial <- struct{}{}
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	eventually(t, func() bool { return n.Load() == 2 })
	if took := time.Since(asked); took > 500*time.Millisecond {
		t.Fatalf("connected again %s after asking", took)
	}
}

// A ping that is out (a slow link's heartbeat may wait a long time for its pong) does not hold up "Reconnect now".
func TestRedialDoesNotWaitForAPingThatIsOut(t *testing.T) {
	srv, n := fakeHub(t, func(c *websocket.Conn) {
		_ = c.Write(context.Background(), websocket.MessageText, []byte(`{"type":"endpoints","ping_ms":50,"ping_timeout_ms":60000}`))
		time.Sleep(time.Minute) // never reads: the ping stays out
	})
	redial := make(chan struct{}, 1)
	ch := runAgainst(t, srv, "jd_x", Options{Redial: redial})
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	time.Sleep(300 * time.Millisecond) // the hub's first ping went 50 ms after its endpoints
	asked := time.Now()
	redial <- struct{}{}
	eventually(t, func() bool { return n.Load() == 2 })
	if took := time.Since(asked); took > 2*time.Second {
		t.Fatalf("connected again %s after asking", took)
	}
}

func sendJSON(t *testing.T, c *websocket.Conn, raw string) {
	t.Helper()
	if err := c.Write(context.Background(), websocket.MessageText, []byte(raw)); err != nil {
		t.Error(err)
	}
}

func TestLinkKeepsWhatTheHubSaysItIs(t *testing.T) {
	next := make(chan string, 4)
	srv, _ := fakeHub(t, func(c *websocket.Conn) {
		go func() {
			for raw := range next {
				sendJSON(t, c, raw)
			}
		}()
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	})
	path := filepath.Join(t.TempDir(), "link.json")
	redial := make(chan struct{}, 1)
	ch := runAgainst(t, srv, "jd_x", Options{Path: path, Redial: redial})
	waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	// A name with control characters in it comes out as one plain line.
	next <- `{"type":"endpoints","endpoints":[{"url":"` + srv.URL + `","kind":"lan"}],"hub_name":"  家里\u001b[2J\n ","mesh_hubs":3}`
	s := waitFor(t, ch, func(s Status) bool { return s.MeshHubs == 3 })
	if s.HubName != "家里 [2J" || s.State != "connected" {
		t.Fatalf("status %+v", s)
	}
	// Only the name changed: the candidates stay, the mesh size too.
	next <- `{"type":"endpoints","hub_name":"书房"}`
	s = waitFor(t, ch, func(s Status) bool { return s.HubName == "书房" })
	if s.MeshHubs != 3 || len(s.Endpoints) != 1 {
		t.Fatalf("status %+v", s)
	}
	c, err := Load(path)
	if err != nil || c.HubName != "书房" || len(c.Endpoints) != 1 || c.Endpoints[0].URL != srv.URL {
		t.Fatalf("link.json %+v %v", c, err)
	}
	// Reconnecting, the name is known before the hub says it again; the mesh size is not.
	redial <- struct{}{}
	s = waitFor(t, ch, func(s Status) bool { return s.State == "connecting" })
	if s.HubName != "书房" || s.MeshHubs != 0 {
		t.Fatalf("status %+v", s)
	}
}

func TestLinkWithAHubThatSaysNothingAboutItself(t *testing.T) {
	srv, _ := fakeHub(t, func(c *websocket.Conn) {
		sendJSON(t, c, `{"type":"endpoints","endpoints":[{"url":"https://hub.example.com:18662","kind":"name"}]}`)
		sendJSON(t, c, `{"type":"node","online":true}`)
		for {
			if _, _, err := c.Read(context.Background()); err != nil {
				return
			}
		}
	})
	path := filepath.Join(t.TempDir(), "link.json")
	ch := runAgainst(t, srv, "jd_x", Options{Path: path})
	s := waitFor(t, ch, func(s Status) bool { return s.State == "connected" && s.Node == "online" && len(s.Endpoints) == 1 })
	if s.HubName != "" || s.MeshHubs != 0 || s.Endpoints[0].URL != "https://hub.example.com:18662" || s.Attention != nil {
		t.Fatalf("status %+v", s)
	}
	if c, err := Load(path); err != nil || c.HubName != "" {
		t.Fatalf("link.json %+v %v", c, err)
	}
}

func TestRunLockAndSavedStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	if Running(path) {
		t.Fatal("running before anything ran")
	}
	release, err := LockRun(path)
	if err != nil {
		t.Fatal(err)
	}
	if !Running(path) {
		t.Fatal("not running while locked")
	}
	if _, err := LockRun(path); !errors.Is(err, ErrRunning) {
		t.Fatalf("a second one: %v", err)
	}
	release()
	if Running(path) {
		t.Fatal("running after release")
	}
	now := time.Now().UTC().Truncate(time.Second)
	if err := WriteStatus(path, SavedStatus{Status: Status{State: "connected", HubName: "家里", MeshHubs: 2}, PID: 7, Updated: now}); err != nil {
		t.Fatal(err)
	}
	st, err := ReadStatus(path)
	if err != nil || st.State != "connected" || st.HubName != "家里" || st.MeshHubs != 2 || st.PID != 7 || !st.Updated.Equal(now) {
		t.Fatalf("%+v %v", st, err)
	}
	if StatusPath(path) != filepath.Join(filepath.Dir(path), "link.status.json") || LogPath(path) != filepath.Join(filepath.Dir(path), "link.log") {
		t.Fatal(StatusPath(path), LogPath(path))
	}
}

func TestRaceCalledOffIsAnError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got, _, err := race(ctx, []Endpoint{{URL: "https://a", Kind: "lan"}, {URL: "https://b", Kind: "name"}},
		func(ctx context.Context, ep Endpoint) (*int, error) { return nil, ctx.Err() }, func(*int) {})
	if got != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("%v %v: a race that was called off must not look like a win", got, err)
	}
}

func TestParsePairLinkWithoutAPinIsAReverseProxy(t *testing.T) {
	q := url.Values{"url": {"https://Hub.Example.com/prefix/"}, "token": {"jp_x"}, "hub": {"h1"}, "machine": {"laptop"},
		"alt": {"https://hub.example.com:443/prefix"}}
	token, c, err := parsePairLink("persona://link?" + q.Encode())
	if err != nil || token != "jp_x" || c.FP != "" || c.Trust != TrustSystem || !c.SystemTrust() {
		t.Fatalf("%v %+v", err, c)
	}
	// The path stays, the port is spelled out as in the hub's list (the alt is the same address).
	if eps := ranked(c.Endpoints); len(eps) != 1 || eps[0].URL != "https://hub.example.com:443/prefix" || eps[0].Kind != "name" || eps[0].Manual {
		t.Fatalf("candidates %+v", eps)
	}
	if got := wsURL(c.Endpoints[0].URL, StreamPath+"?id=s1"); got != "wss://hub.example.com:443/prefix/api/v1/link/stream?id=s1" {
		t.Fatalf("stream URL %s", got)
	}
	q.Set("url", "https://hub.example.com:8443")
	if _, c, err = parsePairLink("persona://link?" + q.Encode()); err != nil || c.Endpoints[0].URL != "https://hub.example.com:8443" {
		t.Fatalf("%v %+v", err, c)
	}
	// No pin and no https: there is nothing the link may use.
	for _, bad := range []string{"http://hub.example.com/prefix", "", "https://user@hub.example.com/prefix", "https://hub.example.com/p?x=1"} {
		q.Set("url", bad)
		if _, _, err := parsePairLink("persona://link?" + q.Encode()); err == nil || !strings.Contains(err.Error(), "HTTPS") {
			t.Errorf("%q: %v", bad, err)
		}
	}
	// With a pin the hub's own TLS port is used, never the proxy's path, and the trust stays the pin.
	q.Set("url", "https://hub.example.com/prefix")
	q.Set("fp", "pin")
	q.Set("tls", "18662")
	q.Del("alt")
	if _, c, err = parsePairLink("persona://link?" + q.Encode()); err != nil || c.Trust != "" || c.SystemTrust() ||
		len(c.Endpoints) != 1 || c.Endpoints[0].URL != "https://hub.example.com:18662" {
		t.Fatalf("%v %+v", err, c)
	}
}

func TestLoadTakesASystemTrustPairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "link.json")
	_ = Update(path, func(c *Config) error {
		*c = Config{Machine: "laptop", Token: "jd_x", Endpoints: []Endpoint{{URL: "https://hub.example.com:443/prefix", Kind: "name"}}}
		return nil
	})
	if _, err := Load(path); err == nil {
		t.Fatal("neither a pin nor system trust loaded")
	}
	_ = Update(path, func(c *Config) error { c.Trust = TrustSystem; return nil })
	if c, err := Load(path); err != nil || !c.SystemTrust() {
		t.Fatalf("%+v %v", c, err)
	}
}

// testCA is an httptest TLS server's certificate as the only "system" CA.
func testCA(t *testing.T, srv *httptest.Server) {
	t.Helper()
	pool := x509.NewCertPool()
	pool.AddCert(srv.Certificate())
	t.Cleanup(UseRootsForTesting(pool))
}

func TestSystemTrustChecksTheCAAndTheHostName(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	get := func(raw string) error {
		resp, err := systemTrusted(nil).Get(raw)
		if err == nil {
			resp.Body.Close()
		}
		return err
	}
	// Not in the (system) pool: refused.
	t.Cleanup(UseRootsForTesting(x509.NewCertPool()))
	if err := get(srv.URL); err == nil || describe(err) != "证书不受系统信任（系统证书校验）" {
		t.Fatalf("an unknown CA: %v (%s)", err, describe(err))
	}
	testCA(t, srv)
	if err := get(srv.URL); err != nil {
		t.Fatal(err)
	}
	// The certificate names 127.0.0.1 and example.com: the same server under another name is refused.
	port := srv.Listener.Addr().(*net.TCPAddr).Port
	if err := get("https://localhost:" + strconv.Itoa(port)); err == nil || describe(err) != "证书上的域名对不上（系统证书校验）" {
		t.Fatalf("another host name: %v (%s)", err, describe(err))
	}
}

func TestAPinNeverFallsBackToSystemTrust(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	testCA(t, srv) // the system would trust it...
	resp, err := hubClient(&Config{FP: "not-its-pin", Trust: TrustSystem}, nil).Get(srv.URL)
	if err == nil {
		resp.Body.Close()
		t.Fatal("a pinned pairing talked to a hub the system trusts but the pin does not")
	}
	if !errors.Is(err, errPin) {
		t.Fatalf("error %v", err)
	}
}

// A hub behind a reverse proxy at /prefix: the link and its streams go under the prefix, checked with the system's CAs.
func TestLinkUnderAProxyPath(t *testing.T) {
	var paths sync.Map
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths.Store(r.URL.Path, true)
		if r.URL.Path != "/prefix"+Path || r.Header.Get("Authorization") != "Bearer jd_x" {
			http.Error(w, "no", http.StatusNotFound)
			return
		}
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			if _, _, err := c.Read(r.Context()); err != nil {
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	testCA(t, srv)
	path := filepath.Join(t.TempDir(), "link.json")
	_ = Update(path, func(c *Config) error {
		*c = Config{Machine: "laptop", Trust: TrustSystem, Token: "jd_x", Endpoints: []Endpoint{{URL: srv.URL + "/prefix", Kind: "lan"}}}
		return nil
	})
	ch := make(chan Status, 64)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, Options{Path: path, Status: func(s Status) {
			select {
			case ch <- s:
			default:
			}
		}})
	}()
	t.Cleanup(func() { cancel(); <-done })
	s := waitFor(t, ch, func(s Status) bool { return s.State == "connected" })
	if s.Trust != TrustSystem || s.Address != srv.URL+"/prefix" {
		t.Fatalf("status %+v", s)
	}
	if _, ok := paths.Load("/prefix" + Path); !ok {
		t.Fatal("the link did not go under the prefix")
	}
}
