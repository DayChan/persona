//go:build unix

package hubedge

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// The test binary doubles as the hub: run by the edge under one of these names (a symlink to it), it is a slot.
//
//	v1, v2         serve; GET / answers "<name> <slot> <remote> <local>"; /crash exits 1; /switch?to=&expect=&kind= asks
//	               the edge to switch (the answer says how that went)
//	bad-standby    exits before it says standby
//	bad-activate   says standby, then fails when activated
const envTestPort = "HUBEDGE_TEST_PORT"

func TestMain(m *testing.M) {
	if os.Getenv(EnvSlot) != "" {
		os.Exit(fakeSlot(filepath.Base(os.Args[0])))
	}
	os.Exit(m.Run())
}

func fakeSlot(name string) int {
	s, err := FromEnv()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	build := int64(0)
	if n, err := strconv.Atoi(strings.TrimPrefix(name, "v")); err == nil {
		build = int64(n)
	}
	s.Hello(name, build)
	if name == "bad-standby" {
		return 1
	}
	if s.Standby {
		if err := s.StandbyReady(context.Background()); err != nil {
			return 0
		}
	}
	if name == "bad-activate" {
		s.Failed("cannot migrate")
		return 1
	}
	ln, err := s.Listen("127.0.0.1:" + os.Getenv(envTestPort))
	if err != nil {
		s.Failed(err.Error())
		return 1
	}
	srv := &http.Server{ConnContext: func(ctx context.Context, c net.Conn) context.Context {
		return context.WithValue(ctx, localKey{}, c.LocalAddr().String())
	}}
	srv.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/crash":
			os.Exit(1)
		case "/switch":
			q := r.URL.Query()
			expect, _ := strconv.ParseInt(q.Get("expect"), 10, 64)
			err := s.Switch(context.Background(), q.Get("to"), "", q.Get("kind"), expect)
			fmt.Fprintf(w, "switch: %v", err)
			return
		case "/confirm":
			s.Confirmed()
		}
		fmt.Fprintf(w, "%s %s %s %s", name, s.Name, r.RemoteAddr, r.Context().Value(localKey{}))
	})
	go func() { _ = srv.Serve(ln) }()
	s.Ready()
	<-s.Released()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	return 0
}

type localKey struct{}

type testEdge struct {
	t       *testing.T
	dir     string
	url     string
	control string
	cancel  context.CancelFunc
	done    chan error
	logs    *strings.Builder
	mu      sync.Mutex
}

// binary is the test binary under name (what the fake slot does).
func (te *testEdge) binary(name string) string {
	p := filepath.Join(te.dir, name)
	if _, err := os.Lstat(p); err != nil {
		self, err := os.Executable()
		if err != nil {
			te.t.Fatal(err)
		}
		if err := os.Symlink(self, p); err != nil {
			te.t.Fatal(err)
		}
	}
	return p
}

func startEdge(t *testing.T, first string, tweak func(*Options)) *testEdge {
	t.Helper()
	dir, err := os.MkdirTemp("", "edge")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	_, port, _ := net.SplitHostPort(ln.Addr().String())
	t.Setenv(envTestPort, port)
	te := &testEdge{t: t, dir: dir, url: "http://" + ln.Addr().String(), control: filepath.Join(dir, "control.sock"),
		done: make(chan error, 1), logs: &strings.Builder{}}
	o := Options{Listeners: []net.Listener{ln}, Binary: te.binary(first), RunDir: filepath.Join(dir, "run"),
		Control: te.control, Version: "edge-test",
		Logf: func(f string, a ...any) {
			te.mu.Lock()
			fmt.Fprintf(te.logs, f+"\n", a...)
			te.mu.Unlock()
		},
		HoldMax: 10 * time.Second, StandbyWait: 10 * time.Second, ReleaseWait: 5 * time.Second, ActivateWait: 10 * time.Second}
	if tweak != nil {
		tweak(&o)
	}
	ctx, cancel := context.WithCancel(context.Background())
	te.cancel = cancel
	go func() { te.done <- Run(ctx, o) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-te.done:
		case <-time.After(15 * time.Second):
			t.Error("the edge did not stop")
		}
		if t.Failed() {
			te.mu.Lock()
			t.Logf("edge log:\n%s", te.logs.String())
			te.mu.Unlock()
		}
	})
	te.waitFor(first)
	return te
}

var client = &http.Client{Timeout: 15 * time.Second, Transport: &http.Transport{DisableKeepAlives: true}}

func (te *testEdge) get(path string) (string, error) {
	resp, err := client.Get(te.url + path)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

// waitFor waits until GET / is answered by the slot named name.
func (te *testEdge) waitFor(name string) string {
	te.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	var last string
	for time.Now().Before(deadline) {
		body, err := te.get("/")
		if err == nil && strings.HasPrefix(body, name+" ") {
			return body
		}
		last = fmt.Sprint(body, err)
		time.Sleep(50 * time.Millisecond)
	}
	te.t.Fatalf("%s never answered; last: %s", name, last)
	return ""
}

// The edge relays to the slot and says whom: the slot sees the client's address and the one it connected to.
func TestRelaySaysWhom(t *testing.T) {
	te := startEdge(t, "v1", nil)
	body := te.waitFor("v1")
	f := strings.Fields(body)
	if len(f) != 4 || !strings.HasPrefix(f[2], "127.0.0.1:") || f[3] != strings.TrimPrefix(te.url, "http://") {
		t.Fatalf("GET / = %q, want v1 <slot> 127.0.0.1:<port> %s", body, strings.TrimPrefix(te.url, "http://"))
	}
}

// A switch moves the route to the new binary with no request refused or failed meanwhile.
func TestSwitchLosesNoRequest(t *testing.T) {
	te := startEdge(t, "v1", nil)
	var failed atomic.Int32
	var served atomic.Int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if body, err := te.get("/"); err != nil || !strings.HasPrefix(body, "v") {
					failed.Add(1)
					t.Logf("during the switch: %q %v", body, err)
				} else {
					served.Add(1)
				}
			}
		}()
	}
	time.Sleep(200 * time.Millisecond)
	var phases []string
	done, err := Switch(te.control, te.binary("v2"), te.binary("v1"), time.Minute, func(p string) { phases = append(phases, p) })
	if err != nil {
		t.Fatalf("switch: %v", err)
	}
	if done.Build != 2 {
		t.Fatalf("serving build %d after the switch, want 2", done.Build)
	}
	time.Sleep(200 * time.Millisecond)
	close(stop)
	wg.Wait()
	te.waitFor("v2")
	if failed.Load() != 0 {
		t.Fatalf("%d of %d requests failed during the switch", failed.Load(), failed.Load()+served.Load())
	}
	if strings.Join(phases, ",") != "starting,handing over,serving" {
		t.Fatalf("phases %v", phases)
	}
	st, err := Status(te.control)
	if err != nil || st.Build != 2 || st.Edge != "edge-test" || st.Proto != Proto {
		t.Fatalf("status %+v %v", st, err)
	}
}

// A binary that does not start never interrupts the one serving.
func TestSwitchToABinaryThatDoesNotStart(t *testing.T) {
	te := startEdge(t, "v1", nil)
	before := te.waitFor("v1")
	if _, err := Switch(te.control, te.binary("bad-standby"), "", time.Minute, nil); err == nil {
		t.Fatal("switching to a binary that exits: no error")
	}
	if after := te.waitFor("v1"); strings.Fields(after)[1] != strings.Fields(before)[1] {
		t.Fatalf("the slot serving changed: %q, then %q", before, after)
	}
}

// One that fails while taking over is replaced by the binary to go back to.
func TestSwitchFailingToTakeOverGoesBack(t *testing.T) {
	te := startEdge(t, "v1", nil)
	_, err := Switch(te.control, te.binary("bad-activate"), te.binary("v1"), time.Minute, nil)
	if err == nil || !strings.Contains(err.Error(), "cannot migrate") {
		t.Fatalf("switch: %v, want it to fail with the slot's reason", err)
	}
	te.waitFor("v1")
}

// The serving slot asks to be replaced (the hub's self-update): an unexpected build is refused, the right one serves.
func TestSlotAsksForTheSwitch(t *testing.T) {
	te := startEdge(t, "v1", nil)
	q := url.Values{"to": {te.binary("v2")}, "expect": {"9"}, "kind": {KindUpdate}}
	if body, err := te.get("/switch?" + q.Encode()); err != nil || !strings.Contains(body, "not 9") {
		t.Fatalf("switch to the wrong build: %q %v", body, err)
	}
	te.waitFor("v1")
	q.Set("expect", "2")
	go func() { _, _ = te.get("/switch?" + q.Encode()) }() // answered by v1 as it is released, or cut off
	te.waitFor("v2")
}

// An update not confirmed in time goes back to what RestoreUpdate says.
func TestUnconfirmedUpdateGoesBack(t *testing.T) {
	restored := make(chan int64, 1)
	var te *testEdge
	te = startEdge(t, "v1", func(o *Options) {
		o.ConfirmWait = time.Second
		o.RestoreUpdate = func(build int64, cause error) (string, bool) {
			restored <- build
			return te.binary("v1"), true
		}
	})
	q := url.Values{"to": {te.binary("v2")}, "expect": {"2"}, "kind": {KindUpdate}}
	go func() { _, _ = te.get("/switch?" + q.Encode()) }()
	te.waitFor("v2")
	select {
	case b := <-restored:
		if b != 2 {
			t.Fatalf("restored build %d, want 2", b)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the unconfirmed update was not undone")
	}
	te.waitFor("v1")
}

// A confirmed update stays.
func TestConfirmedUpdateStays(t *testing.T) {
	var calls atomic.Int32
	var te *testEdge
	te = startEdge(t, "v1", func(o *Options) {
		o.ConfirmWait = 2 * time.Second
		o.RestoreUpdate = func(int64, error) (string, bool) { calls.Add(1); return te.binary("v1"), true }
	})
	q := url.Values{"to": {te.binary("v2")}, "expect": {"2"}, "kind": {KindUpdate}}
	go func() { _, _ = te.get("/switch?" + q.Encode()) }()
	te.waitFor("v2")
	if _, err := te.get("/confirm"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(3 * time.Second)
	te.waitFor("v2")
	if calls.Load() != 0 {
		t.Fatal("a confirmed update was undone")
	}
}

// A slot that dies is started again. (A connection the dying slot had already taken is not replayed: it may have
// acted on the request; only those a released slot never answered are.)
func TestCrashedSlotStartsAgain(t *testing.T) {
	te := startEdge(t, "v1", nil)
	before := strings.Fields(te.waitFor("v1"))[1]
	_, _ = te.get("/crash")
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if body, err := te.get("/"); err == nil && strings.HasPrefix(body, "v1 ") && strings.Fields(body)[1] != before {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("no new slot after the crash")
}

// Connections queued in a slot's socket when the edge releases it go to the next slot: a burst of new connections
// during many switches all get answers.
func TestSwitchesUnderLoad(t *testing.T) {
	te := startEdge(t, "v1", nil)
	var failed, served atomic.Int32
	stop := make(chan struct{})
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				select {
				case <-stop:
					return
				default:
				}
				if body, err := te.get("/"); err != nil || !strings.HasPrefix(body, "v") {
					failed.Add(1)
					t.Logf("%q %v", body, err)
				} else {
					served.Add(1)
				}
			}
		}()
	}
	for i := 0; i < 6; i++ {
		to := "v2"
		if i%2 == 1 {
			to = "v1"
		}
		if _, err := Switch(te.control, te.binary(to), "", time.Minute, nil); err != nil {
			t.Fatalf("switch %d: %v", i, err)
		}
	}
	close(stop)
	wg.Wait()
	if failed.Load() != 0 || served.Load() == 0 {
		t.Fatalf("%d failed, %d served across 6 switches", failed.Load(), served.Load())
	}
	t.Logf("%d requests across 6 switches, none failed", served.Load())
}

// Only what cannot have done anything on the released slot is replayed on the next one.
func TestReplayable(t *testing.T) {
	for sent, want := range map[string]bool{
		"":                               true,
		"\x16\x03\x01\x02\x00":           true, // TLS ClientHello: the slot never answered, so no request ran
		"GET /api/v1/ws HTTP/1.1\r\n":    true,
		"HEAD / HTTP/1.1\r\n":            true,
		"OPTIONS / HTTP/1.1\r\n":         true,
		"POST /api/v1/send HTTP/1.1\r\n": false,
		"DELETE /x HTTP/1.1\r\n":         false,
		"PUT /x HTTP/1.1\r\n":            false,
	} {
		if got := replayable([]byte(sent)); got != want {
			t.Errorf("replayable(%q) = %v, want %v", sent, got, want)
		}
	}
}

// A slot that answers early and half-closes (net/http after a 413, or a 401 on an upload whose body it did not read)
// still gets what the client sends after the answer: the edge passes the half-close on and keeps the other way open.
func TestRelayPassesTheSlotsHalfCloseOn(t *testing.T) {
	client, edgeClient := tcpPair(t)
	edgeUp, slot := tcpPair(t)
	r := &relay{client: edgeClient, up: edgeUp}
	go r.fromClient()
	go r.toClient(func() net.Conn { return nil })

	slotRead := make(chan string, 1)
	go func() {
		_, _ = slot.Write([]byte("answer"))
		_ = slot.CloseWrite()
		b, _ := io.ReadAll(slot)
		slotRead <- string(b)
	}()
	_ = client.SetDeadline(time.Now().Add(10 * time.Second))
	if b, err := io.ReadAll(client); err != nil || string(b) != "answer" {
		t.Fatalf("the client read %q, %v; want the answer, then the slot's half-close", b, err)
	}
	if _, err := client.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	_ = client.CloseWrite()
	select {
	case got := <-slotRead:
		if got != "tail" {
			t.Errorf("after its half-close the slot read %q, want %q", got, "tail")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("the slot never saw the client's half-close")
	}
}

// A client that neither sends nor half-closes after the slot's half-close does not hold the relay open.
func TestRelayHalfCloseLingerIsBounded(t *testing.T) {
	_, edgeClient := tcpPair(t)
	edgeUp, slot := tcpPair(t)
	r := &relay{client: edgeClient, up: edgeUp, linger: 100 * time.Millisecond}
	go r.fromClient()
	go r.toClient(func() net.Conn { return nil })

	_, _ = slot.Write([]byte("answer"))
	_ = slot.CloseWrite()
	_ = slot.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.ReadAll(slot); err != nil {
		t.Fatalf("the slot's read ended with %v, want EOF once the linger is over", err)
	}
}

// tcpPair is both ends of a loopback TCP connection.
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, _ := ln.Accept()
		accepted <- c
	}()
	a, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	b := <-accepted
	if b == nil {
		t.Fatal("the loopback listener accepted nothing")
	}
	t.Cleanup(func() { _ = a.Close(); _ = b.Close() })
	return a.(*net.TCPConn), b.(*net.TCPConn)
}
