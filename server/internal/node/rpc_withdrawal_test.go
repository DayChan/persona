package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/rpc"
)

// The node's actual connection Peer over an owned pipe transport: hook mcp.calls
// stopped together (several hooks killed at once) must all be withdrawn on the
// hub without interrupting the healthy owned transport, which for a stdio node
// would end the process.
func TestNodeRPCConcurrentHookWithdrawalsKeepOwnedTransport(t *testing.T) {
	const wait = 5 * time.Second
	for _, n := range []int{2, 4} {
		for rep := 0; rep < 30; rep++ {
			t.Run(fmt.Sprintf("n%d/%02d", n, rep), func(t *testing.T) {
				hubR, nodeW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				nodeR, hubW, err := os.Pipe()
				if err != nil {
					t.Fatal(err)
				}
				var interrupted atomic.Bool
				owned := &ownedRPCTransport{Reader: nodeR, Writer: nodeW, interrupt: func() {
					interrupted.Store(true)
					_ = nodeW.Close()
					_ = nodeR.Close()
				}}
				writer := &updateResponseWriter{Writer: owned, responseWritten: func(string) { t.Error("FAIL withdrawal acknowledged an update") }}
				node := newConnectionPeer(writer, owned)
				node.OnRequest = func(context.Context, string, json.RawMessage) (any, error) { return "pong", nil }
				hub := rpc.New("h", hubW)
				admitted, withdrawn := make(chan struct{}, n), make(chan struct{}, n)
				hub.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
					if method != "mcp.call" {
						return "pong", nil
					}
					admitted <- struct{}{}
					<-ctx.Done()
					withdrawn <- struct{}{}
					return nil, ctx.Err()
				}
				nodeServed, hubServed := make(chan struct{}), make(chan struct{})
				go func() { _ = node.Serve(context.Background(), owned); close(nodeServed) }()
				go func() { _ = hub.Serve(context.Background(), hubR); close(hubServed) }()
				t.Cleanup(func() {
					node.Close(rpc.ErrClosed)
					hub.Close(rpc.ErrClosed)
					for _, f := range []*os.File{hubR, hubW, nodeR, nodeW} {
						_ = f.Close()
					}
					for _, ch := range []chan struct{}{nodeServed, hubServed} {
						select {
						case <-ch:
						case <-time.After(wait):
							t.Error("TIMEOUT reader exit")
						}
					}
				})
				await := func(ch <-chan struct{}, what string) {
					t.Helper()
					timeout := time.After(wait)
					for i := 0; i < n; i++ {
						select {
						case <-ch:
						case <-timeout:
							t.Fatalf("TIMEOUT %s %d/%d", what, i, n)
						}
					}
				}
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				var wg sync.WaitGroup
				for i := 0; i < n; i++ {
					wg.Go(func() {
						if err := node.Call(parent, "mcp.call", map[string]any{"name": "x"}, nil); !errors.Is(err, context.Canceled) {
							t.Error("FAIL hook caller identity:", err)
						}
					})
				}
				await(admitted, "hub admission")
				cancel()
				wg.Wait()
				await(withdrawn, "hub withdrawal")
				select {
				case <-hubServed:
					t.Fatal("FAIL hub read EOF: owned node transport ended")
				default:
				}
				if interrupted.Load() || node.Err() != nil {
					t.Fatal("FAIL healthy owned transport interrupted", node.Err())
				}
				ctx, stop := context.WithTimeout(context.Background(), wait)
				defer stop()
				var out string
				if err := node.Call(ctx, "ping", nil, &out); err != nil || out != "pong" {
					t.Fatal("FAIL node call after withdrawals:", out, err)
				}
				if err := hub.Call(ctx, "ping", nil, &out); err != nil || out != "pong" {
					t.Fatal("FAIL hub call after withdrawals:", out, err)
				}
			})
		}
	}
}
