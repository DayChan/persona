package hub

import (
	"context"
	"fmt"
	"log"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// These are shutdown policy budgets, not measured service latencies.
const (
	// DefaultShutdownTimeout is the hub's whole shutdown without hub.json's shutdown_timeout_seconds (a container's
	// supervisor, internal/supervise, waits that long and a little more before it kills the hub).
	DefaultShutdownTimeout = 8 * time.Second
	shutdownStepTimeout    = 2 * time.Second
	websocketCloseGrace    = 500 * time.Millisecond
)

type shutdownBudget struct {
	once    sync.Once
	started chan struct{}
	timeout time.Duration
	ctx     context.Context
	cancel  context.CancelFunc
}

func newShutdownBudget(cfg *Config) *shutdownBudget {
	timeout := DefaultShutdownTimeout
	if cfg.ShutdownTimeoutSeconds > 0 {
		timeout = time.Duration(cfg.ShutdownTimeoutSeconds) * time.Second
	}
	return &shutdownBudget{started: make(chan struct{}), timeout: timeout}
}

func (b *shutdownBudget) start() context.Context {
	b.once.Do(func() {
		b.ctx, b.cancel = context.WithTimeout(context.Background(), b.timeout)
		close(b.started)
	})
	return b.ctx
}

func (b *shutdownBudget) close() {
	select {
	case <-b.started:
		b.cancel()
	default:
	}
}

// run also bounds cleanup that fails to honor its context, including locks or pool borrowers.
// The command returns this error to main, which exits the process.
func (b *shutdownBudget) run(ctx context.Context, run func() error) error {
	done := make(chan error, 1)
	go func() { done <- run() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		b.start()
	case <-b.started:
	}
	select {
	case err := <-done:
		return err
	case <-b.ctx.Done():
		err := fmt.Errorf("hub shutdown exceeded total budget %s: %w", b.timeout, b.ctx.Err())
		log.Print(err)
		return err
	}
}

func (b *shutdownBudget) step(h *Hub, name string, stop func(context.Context) error) {
	ctx, cancel := context.WithTimeout(b.start(), shutdownStepTimeout)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- stop(ctx) }()
	select {
	case err := <-done:
		if err != nil {
			h.logf("shutdown %s: %v", name, err)
		}
	case <-ctx.Done():
		h.logf("shutdown %s: %v", name, ctx.Err())
	}
}

func (h *Hub) stopWebSockets(ctx context.Context) error {
	h.mu.Lock()
	h.wsStopping = true
	clients := make([]*client, 0, len(h.wsConnections))
	for c := range h.wsConnections {
		c.stopping.Store(true)
		clients = append(clients, c)
	}
	h.mu.Unlock()
	closeReason := "hub stopping"
	if h.stopping.Load() {
		closeReason = "hub restarting" // after hub_restarting (restart.go)
	}
	links := h.stopLinks() // their streams end now; persona-link comes back to the next hub on the 1001
	h.stopPeerLinks()      // the other hubs of the mesh group hear a 1001 too (peer_link.go)
	buses := h.stopBus()   // the services reconnect to the next hub on the 1001 (bus.go)

	var closes sync.WaitGroup
	for _, c := range clients {
		closes.Add(1)
		go func() {
			defer closes.Done()
			defer c.cancel()
			_ = c.conn.Close(websocket.StatusGoingAway, closeReason)
		}()
	}
	for _, l := range links {
		closes.Add(1)
		go func() {
			defer closes.Done()
			_ = l.conn.Close(websocket.StatusGoingAway, "hub stopping")
		}()
	}
	for _, c := range buses {
		closes.Add(1)
		go func() {
			defer closes.Done()
			_ = c.ws.Close(websocket.StatusGoingAway, "hub stopping")
		}()
	}
	closed := make(chan struct{})
	go func() { closes.Wait(); close(closed) }()
	grace := time.NewTimer(websocketCloseGrace)
	defer grace.Stop()
	select {
	case <-closed:
	case <-ctx.Done():
	case <-grace.C:
	}
	// A suspended phone may never answer the close handshake.
	for _, c := range clients {
		c.cancel()
		_ = c.conn.CloseNow()
	}
	for _, l := range links {
		_ = l.conn.CloseNow()
	}
	for _, c := range buses {
		_ = c.ws.CloseNow()
	}
	done := make(chan struct{})
	go func() {
		<-closed
		h.wsHandlers.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
