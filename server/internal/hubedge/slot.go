package hubedge

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
)

// What the edge tells a slot it starts, in its environment (cleared once read, so nothing the slot starts sees it).
const (
	EnvSlot    = "PERSONA_HUB_SLOT"         // the slot's name: a or b
	EnvRole    = "PERSONA_HUB_SLOT_ROLE"    // standby (wait for activate) or active (serve at once)
	EnvSockets = "PERSONA_HUB_SLOT_SOCKETS" // port=path;port=path: where to listen for each of the edge's ports
)

// The slot's ends of the two pipes: fd 3 reads what the edge says, fd 4 writes to it.
const (
	slotReadFD  = 3
	slotWriteFD = 4
)

// RespawnCode is the exit code with which a slot asks the edge to start it again at once, on the same binary path:
// it restored the binary that ran before an update (selfupdate.go boot) and that one should run instead.
const RespawnCode = 75

// ErrReleased: the edge told the slot to stop (or went away) before it was activated.
var ErrReleased = errors.New("hubedge: released")

// ErrNoSocket: the edge holds no port for that address (it was not in hub.json when the service started, or the edge
// could not listen on it: a LAN address DHCP has moved).
var ErrNoSocket = errors.New("hubedge: the edge has no socket for this port")

// Slot is a hub process the edge started: its end of the edge's pipes and the sockets it serves on.
type Slot struct {
	Name    string
	Standby bool              // started as a standby: StandbyReady before serving
	sockets map[string]string // port -> socket path

	out      *lineWriter
	activate chan struct{}
	released chan struct{}
	failed   chan string // switch_failed reasons

	mu       sync.Mutex
	serving  bool
	actOnce  sync.Once
	relOnce  sync.Once
	switchMu sync.Mutex
}

// FromEnv is the slot this process is, or nil when the edge did not start it.
func FromEnv() (*Slot, error) {
	name := os.Getenv(EnvSlot)
	if name == "" {
		return nil, nil
	}
	s := &Slot{Name: name, Standby: os.Getenv(EnvRole) == "standby",
		sockets: map[string]string{}, activate: make(chan struct{}), released: make(chan struct{}), failed: make(chan string, 1)}
	for _, kv := range strings.Split(os.Getenv(EnvSockets), ";") {
		if port, path, ok := strings.Cut(kv, "="); ok && port != "" && path != "" {
			s.sockets[port] = path
		}
	}
	for _, k := range []string{EnvSlot, EnvRole, EnvSockets} {
		_ = os.Unsetenv(k)
	}
	in, out, err := slotPipes()
	if err != nil {
		return nil, err
	}
	s.out = &lineWriter{w: out}
	msgs := make(chan Msg, 8)
	go readMsgs(in, msgs)
	go s.dispatch(msgs)
	return s, nil
}

func (s *Slot) dispatch(msgs <-chan Msg) {
	for m := range msgs {
		switch m.T {
		case MsgActivate:
			s.actOnce.Do(func() { close(s.activate) })
		case MsgRelease:
			s.release()
		case MsgSwitchFailed:
			select {
			case s.failed <- m.Reason:
			default:
			}
		}
	}
	s.release() // the edge is gone: a slot nobody relays to must not go on holding the hub's machines
}

func (s *Slot) release() { s.relOnce.Do(func() { close(s.released) }) }

// Released is closed once the edge says stop, or goes away.
func (s *Slot) Released() <-chan struct{} { return s.released }

func (s *Slot) send(m Msg) {
	if err := s.out.send(m); err != nil {
		s.release()
	}
}

// Hello says who this slot is; first thing, before anything can fail.
func (s *Slot) Hello(version string, build int64) {
	s.send(Msg{T: MsgHello, Proto: Proto, Version: version, Build: build, PID: os.Getpid(), Slot: s.Name})
}

// Ready says the slot listens on its sockets: the route may move to it.
func (s *Slot) Ready() {
	s.mu.Lock()
	s.serving = true
	s.mu.Unlock()
	s.send(Msg{T: MsgReady})
}

// Serving: Ready was said (the slot may hand over to a new binary; before that it can only exit).
func (s *Slot) Serving() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.serving
}

// Failed says the slot cannot start, why; it exits right after.
func (s *Slot) Failed(reason string) { s.send(Msg{T: MsgFailed, Reason: reason}) }

// Confirmed says the update this slot is the result of is confirmed: the edge's own check of it ends.
func (s *Slot) Confirmed() { s.send(Msg{T: MsgConfirmed}) }

// StandbyReady says a standby slot checked itself, then waits for activate. ErrReleased: told to stop instead.
func (s *Slot) StandbyReady(ctx context.Context) error {
	s.send(Msg{T: MsgStandby})
	select {
	case <-s.activate:
		return nil
	case <-s.released:
		return ErrReleased
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Switch asks the edge to replace this slot by binary. It returns nil once the edge releases this slot (the new one
// is taking over: this process stops), or why the new one did not come up (this slot stays active).
func (s *Slot) Switch(ctx context.Context, binary, prev, kind string, expect int64) error {
	s.switchMu.Lock()
	defer s.switchMu.Unlock()
	select {
	case <-s.failed: // an old answer
	default:
	}
	s.send(Msg{T: MsgSwitch, Binary: binary, Prev: prev, Kind: kind, Expect: expect})
	select {
	case <-s.released:
		return nil
	case why := <-s.failed:
		return fmt.Errorf("%s", why)
	case <-ctx.Done():
		// The release ends the hub's context right after it is closed: that is the switch going through, not
		// giving up on it (a caller undoing the update now would put the old binary back under the new slot).
		select {
		case <-s.released:
			return nil
		default:
			return ctx.Err()
		}
	}
}

// Listen is the slot's listener for the edge's port at addr (host:port as in hub.json).
func (s *Slot) Listen(addr string) (net.Listener, error) {
	_, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("listen %q: %w", addr, err)
	}
	path, ok := s.sockets[port]
	if !ok {
		return nil, fmt.Errorf("listen %q: %w (port %s; restart the hub service after changing its addresses)", addr, ErrNoSocket, port)
	}
	tcp, err := net.ResolveTCPAddr("tcp", addr)
	if err != nil {
		return nil, err
	}
	return ListenSlot(path, tcp)
}
