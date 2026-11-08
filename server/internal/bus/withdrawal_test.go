package bus

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/rpc"
)

const busWithdrawalWait = 5 * time.Second

// withdrawalHub is a websocket hub whose app.call waits for its caller to
// withdraw it; ping answers at once. It counts handshakes and withdrawals.
type withdrawalHub struct {
	hellos    atomic.Int32
	admitted  chan struct{}
	withdrawn chan struct{}
}

func (f *withdrawalHub) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != Path || r.Header.Get("Authorization") == "" {
		http.NotFound(w, r)
		return
	}
	ws, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer ws.CloseNow()
	conn := NewConn(r.Context(), ws)
	p := rpc.New("h", conn)
	p.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
		switch method {
		case "manager.hello":
			f.hellos.Add(1)
			return map[string]any{"name": "n"}, nil
		case "app.call":
			f.admitted <- struct{}{}
			<-ctx.Done()
			f.withdrawn <- struct{}{}
			return nil, ctx.Err()
		}
		return "pong", nil
	}
	_ = p.Serve(r.Context(), conn)
}

func startWithdrawalClient(t *testing.T, f *withdrawalHub) (*Client, context.Context) {
	t.Helper()
	srv := httptest.NewServer(f)
	c := &Client{Hub: srv.URL, Token: "t", HelloMethod: "manager.hello", Cancels: map[string]bool{"app.call": true},
		Backoff: 10 * time.Millisecond, MaxBackoff: 50 * time.Millisecond}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	ran := make(chan struct{})
	go func() { _ = c.Run(ctx); close(ran) }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-ran:
		case <-time.After(busWithdrawalWait):
			t.Error("TIMEOUT client Run exit")
		}
		srv.CloseClientConnections()
		srv.Close()
	})
	wctx, stop := context.WithTimeout(ctx, busWithdrawalWait)
	defer stop()
	if err := c.Wait(wctx); err != nil {
		t.Fatal("TIMEOUT first handshake", err)
	}
	return c, ctx
}

func busAwait(t *testing.T, ch <-chan struct{}, n int, what string) {
	t.Helper()
	timeout := time.After(busWithdrawalWait)
	for i := 0; i < n; i++ {
		select {
		case <-ch:
		case <-timeout:
			t.Fatalf("TIMEOUT %s %d/%d", what, i, n)
		}
	}
}

// The assistant manager's bus opts app.call into withdrawal. Several app.calls
// stopped together by one parent context on a healthy websocket must all be
// withdrawn on the hub, and the same session must keep answering calls.
func TestClientConcurrentWithdrawalsKeepSession(t *testing.T) {
	for _, n := range []int{2, 8} {
		for rep := 0; rep < 10; rep++ {
			t.Run(fmt.Sprintf("n%d/%d", n, rep), func(t *testing.T) {
				f := &withdrawalHub{admitted: make(chan struct{}, n), withdrawn: make(chan struct{}, n)}
				c, ctx := startWithdrawalClient(t, f)
				parent, cancel := context.WithCancel(ctx)
				defer cancel()
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					wg.Go(func() {
						if err := c.Call(parent, "app.call", nil, nil); !errors.Is(err, context.Canceled) {
							t.Error("FAIL caller identity:", err)
						}
					})
				}
				busAwait(t, f.admitted, n, "hub admission")
				cancel()
				wg.Wait()
				busAwait(t, f.withdrawn, n, "hub withdrawal")
				cctx, stop := context.WithTimeout(ctx, busWithdrawalWait)
				defer stop()
				var out string
				if err := c.Call(cctx, "ping", nil, &out); err != nil || out != "pong" {
					t.Fatal("FAIL bus unusable after withdrawals:", out, err)
				}
				if h := f.hellos.Load(); h != 1 {
					t.Fatalf("FAIL healthy session replaced: %d handshakes", h)
				}
			})
		}
	}
}

// Whatever logically closes the client's peer, the session must end so the
// reconnect path runs: a closed peer still reading the websocket would execute
// hub requests it can never answer.
func TestClientClosedPeerEndsSession(t *testing.T) {
	f := &withdrawalHub{admitted: make(chan struct{}, 1), withdrawn: make(chan struct{}, 1)}
	c, ctx := startWithdrawalClient(t, f)
	old := c.Peer()
	old.Close(rpc.ErrClosed)
	deadline := time.After(busWithdrawalWait)
	for {
		if p := c.Peer(); p != nil && p != old {
			break
		}
		select {
		case <-deadline:
			t.Fatalf("FAIL logically closed bus peer kept its session (handshakes=%d)", f.hellos.Load())
		case <-time.After(5 * time.Millisecond):
		}
	}
	cctx, stop := context.WithTimeout(ctx, busWithdrawalWait)
	defer stop()
	var out string
	if err := c.Call(cctx, "ping", nil, &out); err != nil || out != "pong" {
		t.Fatal("FAIL reconnected bus:", out, err)
	}
	if f.hellos.Load() < 2 {
		t.Fatal("FAIL no new handshake")
	}
}
