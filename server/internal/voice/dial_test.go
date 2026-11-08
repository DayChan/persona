package voice

import (
	"context"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// stallListener takes TCP connections and never says a word, like an address that accepts TCP but never answers the
// TLS handshake. It counts the connections it took.
func stallListener(t *testing.T) (string, *atomic.Int32) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	var (
		n     atomic.Int32
		mu    sync.Mutex
		conns []net.Conn
	)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			n.Add(1)
			mu.Lock()
			conns = append(conns, c)
			mu.Unlock()
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		mu.Lock()
		defer mu.Unlock()
		for _, c := range conns {
			_ = c.Close()
		}
	})
	return ln.Addr().String(), &n
}

// refusedAddr is an address nothing listens on.
func refusedAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := ln.Addr().String()
	_ = ln.Close()
	return addr
}

// tlsServer is an httptest TLS server that keeps quiet about handshakes a test makes fail on purpose.
func tlsServer(t *testing.T, h http.Handler) *httptest.Server {
	t.Helper()
	srv := httptest.NewUnstartedServer(h)
	srv.Config.ErrorLog = log.New(io.Discard, "", 0)
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

// upstreamAt makes example.com resolve to addrs, in that order, trusts srv's certificate (none: the system's pool) and
// shortens the per-address budget; all put back, and the shared client's pool and memory emptied, after the test.
func upstreamAt(t *testing.T, srv *httptest.Server, addrs ...string) {
	t.Helper()
	lookup, roots, addrBudget, lastBudget := lookupUpstream, upstreamRoots, tlsAddrBudget, tlsLastBudget
	t.Cleanup(func() {
		lookupUpstream, upstreamRoots, tlsAddrBudget, tlsLastBudget = lookup, roots, addrBudget, lastBudget
		lastGood.Clear()
		directClient.CloseIdleConnections()
	})
	lookupUpstream = func(_ context.Context, host, _ string) ([]string, error) {
		if host != "example.com" {
			return nil, fmt.Errorf("unexpected lookup of %s", host)
		}
		return addrs, nil
	}
	upstreamRoots = nil
	if srv != nil {
		pool := x509.NewCertPool()
		pool.AddCert(srv.Certificate())
		upstreamRoots = pool
	}
	tlsAddrBudget, tlsLastBudget = 300*time.Millisecond, 5*time.Second
}

func TestModelsSkipAnAddressThatNeverAnswersTLS(t *testing.T) {
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/live/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"models":[{"id":"live-1"}]}`))
	}))
	stall, stalled := stallListener(t)
	upstreamAt(t, srv, stall, srv.Listener.Addr().String())
	p := &Provider{ID: "up", URL: "wss://example.com:7001/v1/live"}

	start := time.Now()
	var out struct {
		Models []struct {
			ID string `json:"id"`
		} `json:"models"`
	}
	if err := getJSON(context.Background(), p, "/models", nil, &out); err != nil {
		t.Fatalf("GET /models: %v", err)
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Errorf("GET /models took %s: the stalled address held it past its budget", took)
	}
	if len(out.Models) != 1 || out.Models[0].ID != "live-1" {
		t.Errorf("models = %+v", out.Models)
	}
	// At least: other programs on a developer's machine may probe a listening port too.
	if n := stalled.Load(); n < 1 {
		t.Errorf("the stalled address was not tried first")
	}

	// A new connection goes to the address that worked first: with the stalled one first again, the request would
	// wait out a budget longer than its deadline.
	directClient.CloseIdleConnections()
	tlsAddrBudget = 10 * time.Second
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := getJSON(ctx, p, "/models", nil, &out); err != nil {
		t.Fatalf("second GET /models: %v", err)
	}
}

func TestSessionDialSkipsAnAddressThatNeverAnswersTLS(t *testing.T) {
	srv := tlsServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		typ, b, err := c.Read(r.Context())
		if err != nil {
			return
		}
		_ = c.Write(r.Context(), typ, b)
		_, _, _ = c.Read(r.Context())
	}))
	stall, _ := stallListener(t)
	upstreamAt(t, srv, stall, srv.Listener.Addr().String())
	p := &Provider{ID: "up", URL: "wss://example.com:7002/v1/live"}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	up, err := (&conversation{}).dial(ctx, p, liveWire{}, "")
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer up.CloseNow()
	if err := up.Write(ctx, websocket.MessageText, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if _, b, err := up.Read(ctx); err != nil || string(b) != "hello" {
		t.Fatalf("read = %q, %v", b, err)
	}
}

func TestDialTLSNamesEveryFailedAddress(t *testing.T) {
	srv := tlsServer(t, http.NotFoundHandler())
	stall, _ := stallListener(t)
	refused := refusedAddr(t)
	untrusted := srv.Listener.Addr().String()
	upstreamAt(t, nil, stall, refused, untrusted) // srv's certificate is not trusted

	_, err := dialTLS(context.Background(), "tcp", "example.com:7003")
	if err == nil {
		t.Fatal("dial succeeded with no address that works")
	}
	msg := err.Error()
	for _, want := range []string{stall + ": no TLS handshake within", refused + ": ", untrusted + ": TLS handshake: ", "certificate"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q does not mention %q", msg, want)
		}
	}
}

func TestDialTLSStopsWhenCanceled(t *testing.T) {
	stall, _ := stallListener(t)
	other, _ := stallListener(t)
	upstreamAt(t, nil, stall, other)
	tlsAddrBudget, tlsLastBudget = 10*time.Second, 10*time.Second

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)
	start := time.Now()
	_, err := dialTLS(ctx, "tcp", "example.com:7004")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if took := time.Since(start); took > 2*time.Second {
		t.Errorf("canceled dial returned after %s", took)
	}
}

func TestDialTLSToAnAddressLiteral(t *testing.T) {
	srv := tlsServer(t, http.NotFoundHandler())
	upstreamAt(t, srv) // a lookup would fail the test: an address literal is dialed as it is
	lookupUpstream = func(_ context.Context, host, _ string) ([]string, error) {
		t.Errorf("looked up %s", host)
		return nil, errors.New("no lookup")
	}

	conn, err := dialTLS(context.Background(), "tcp", srv.Listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}
