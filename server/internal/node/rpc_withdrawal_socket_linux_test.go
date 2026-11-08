//go:build linux

package node

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"

	"github.com/nwverse/persona/server/internal/rpc"
)

type nodePacedReader struct {
	r     io.Reader
	every atomic.Int64
	read  atomic.Int64
}

func (s *nodePacedReader) Read(b []byte) (int, error) {
	if len(b) > 4096 {
		b = b[:4096]
	}
	if d := time.Duration(s.every.Load()); d > 0 {
		time.Sleep(d) // throughput shaping, not synchronization
	}
	n, err := s.r.Read(b)
	s.read.Add(int64(n))
	return n, err
}

// The node's actual connection Peer over a real unix socketpair (the attach
// socket, or stdio from a launcher that passes a socketpair), through the
// update-response wrapper: while the node sends a large reply to a hub reading
// steadily at 8–64 KB/s, a hook mcp.call is canceled. The owned transport must
// not be retired for well past the stall window; after the hub speeds up the
// reply arrives whole and the withdrawal is delivered.
func TestNodeRPCSlowUnixSocketReplyKeepsOwnedTransport(t *testing.T) {
	for _, reply := range []int{600 << 10, 2 << 20} {
		for _, rate := range []int{8, 32, 48, 64} {
			t.Run(fmt.Sprintf("%dKB/%dKBps", reply/1024, rate), func(t *testing.T) {
				fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
				if err != nil {
					t.Fatal(err)
				}
				conn := func(fd int) net.Conn {
					f := os.NewFile(uintptr(fd), "socketpair")
					defer f.Close()
					c, err := net.FileConn(f)
					if err != nil {
						t.Fatal(err)
					}
					return c
				}
				nodeConn, hubConn := conn(fds[0]), conn(fds[1])
				var interrupted atomic.Bool
				owned := &ownedRPCTransport{Reader: nodeConn, Writer: nodeConn, interrupt: func() { interrupted.Store(true); _ = nodeConn.Close() }}
				writer := &updateResponseWriter{Writer: owned, responseWritten: func(string) {}}
				node := newConnectionPeer(writer, owned)
				if node.Interrupt == nil || node.Progress == nil {
					t.Fatal("FAIL owned unix socket lacks interrupt or progress")
				}
				node.OnRequest = func(_ context.Context, method string, _ json.RawMessage) (any, error) {
					if method == "big" {
						return strings.Repeat("x", reply), nil
					}
					return "pong", nil
				}
				hub := rpc.New("h", hubConn)
				admitted, withdrawn := make(chan struct{}, 1), make(chan struct{}, 1)
				hub.OnRequest = func(ctx context.Context, method string, _ json.RawMessage) (any, error) {
					if method != "mcp.call" {
						return "pong", nil
					}
					admitted <- struct{}{}
					<-ctx.Done()
					withdrawn <- struct{}{}
					return nil, ctx.Err()
				}
				slow := &nodePacedReader{r: hubConn}
				slow.every.Store(int64(4096 * time.Second / time.Duration(rate*1024)))
				nodeServed, hubServed := make(chan struct{}), make(chan struct{})
				go func() { _ = node.Serve(context.Background(), owned); close(nodeServed) }()
				go func() { _ = hub.Serve(context.Background(), slow); close(hubServed) }()
				t.Cleanup(func() {
					node.Close(rpc.ErrClosed)
					hub.Close(rpc.ErrClosed)
					_ = nodeConn.Close()
					_ = hubConn.Close()
					for _, ch := range []chan struct{}{nodeServed, hubServed} {
						select {
						case <-ch:
						case <-time.After(10 * time.Second):
							t.Error("TIMEOUT reader exit")
						}
					}
				})
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				hook := make(chan error, 1)
				go func() { hook <- node.Call(parent, "mcp.call", nil, nil) }()
				select {
				case <-admitted:
				case <-time.After(10 * time.Second):
					t.Fatal("TIMEOUT hub admission")
				}
				big := make(chan error, 1)
				base := slow.read.Load()
				go func() {
					var s string
					err := hub.Call(context.Background(), "big", nil, &s)
					if err == nil && len(s) != reply {
						err = fmt.Errorf("short reply %d", len(s))
					}
					big <- err
				}()
				for deadline := time.Now().Add(30 * time.Second); slow.read.Load() < base+16<<10; {
					if time.Now().After(deadline) {
						t.Fatal("TIMEOUT reply in progress")
					}
					time.Sleep(time.Millisecond) // polling a progress counter
				}
				cancel()
				select {
				case err := <-hook:
					if !errors.Is(err, context.Canceled) {
						t.Fatal("FAIL hook caller identity:", err)
					}
				case <-time.After(10 * time.Second):
					t.Fatal("TIMEOUT canceled hook")
				}
				select {
				case <-node.Done():
					t.Fatalf("FAIL %d KB/s hub retired the owned node transport: %v (interrupted=%v)", rate, node.Err(), interrupted.Load())
				case err := <-big:
					t.Fatal("FAIL reply finished while still slow:", err)
				case <-time.After(7 * time.Second):
				}
				slow.every.Store(0)
				select {
				case err := <-big:
					if err != nil {
						t.Fatal("FAIL large reply lost:", err)
					}
				case <-time.After(30 * time.Second):
					t.Fatal("TIMEOUT large reply after speed-up")
				}
				select {
				case <-withdrawn:
				case <-time.After(10 * time.Second):
					t.Fatal("TIMEOUT hub withdrawal")
				}
				if node.Err() != nil || interrupted.Load() {
					t.Fatal("FAIL healthy owned transport retired:", node.Err())
				}
				ctx, stop := context.WithTimeout(context.Background(), 10*time.Second)
				defer stop()
				var out string
				if err := node.Call(ctx, "ping", nil, &out); err != nil || out != "pong" {
					t.Fatal("FAIL node call afterwards:", out, err)
				}
			})
		}
	}
}
