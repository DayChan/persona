package assistant

import (
	"context"
	"errors"
	"slices"
	"sync"
	"time"
)

// A computer the user has not said is theirs yet (pending, docs/assistant/onboarding.md 4.2 "Your computers ↔ hub") is connected but
// gets nothing of the assistant's: no Persona-managed keys (pushManaged, the launchers a new connection gets), no
// answers to its requests (the live context, the MCP tools), and it is no machine the assistant runs on or hands work
// to (the hub leaves it out of devices.list and of the hello's machines). The hub says which are pending: its
// machine_trust table is the hub's, and it hands the list to the manager with the computers (devicewire.Config
// Pending, internal/devices PendingMachineIDs); this is that, read again every few seconds and whenever the hub's hello
// changes (a confirm changes its machines).

// MachineTrust says which computers wait to be confirmed (internal/devices.Devices has it); an error while it does
// not know (the hub has not handed the computers over, or could not read its own list).
type MachineTrust interface {
	PendingMachineIDs(ctx context.Context) ([]string, error)
}

// pendingAssistantMachine names a computer to confirm when the main conversation has no confirmed computer.
// The pending list can include other managers' computers: only offer one of our own, excluding work computers.
// Read it fresh so confirmation or deletion is reflected without waiting for the authorization cache to expire.
func (s *Service) pendingAssistantMachine(ctx context.Context) (Device, bool) {
	if s.trustSrc == nil || s.devs == nil {
		return Device{}, false
	}
	pending, err := s.trustSrc.PendingMachineIDs(ctx)
	if err != nil {
		return Device{}, false // unknown trust is not evidence of a computer waiting for confirmation
	}
	var chosen Device
	for _, d := range s.devs.List() {
		if d.ID == "" || d.Trust == "work" || !slices.Contains(pending, d.ID) {
			continue
		}
		if chosen.ID == "" || d.ID < chosen.ID {
			chosen = Device{ID: d.ID, Name: firstNonEmpty(d.Name, d.ID)}
		}
	}
	return chosen, chosen.ID != ""
}

// trustFor is how long a read of the pending computers is used.
const trustFor = 5 * time.Second

// trustRetry is how long after a failed read the list is not asked for again.
const trustRetry = 5 * time.Second

// errMachinePending answers a pending computer's requests.
var errMachinePending = errors.New("this computer is waiting to be confirmed in the Persona app")

// errTrustUnknown answers every computer's requests while the hub has never said which are pending: not the
// computer's fault (the hub's database did not answer, or the computers are not handed over yet).
var errTrustUnknown = errors.New("the hub cannot tell yet whether this computer is confirmed")

type trustBook struct {
	mu       sync.Mutex
	pending  []string
	at       time.Time // the last good read; zero: never, or forgotten
	failedAt time.Time // the last failed read (no new query before trustRetry)
	ok       bool      // a read ever went through
	gen      int       // trustChanged's count: a read begun before one is used once, not kept
	// load lets one read go out at a time (the others wait for it and use it); mu is not held across it.
	load sync.Mutex
}

// machinePending: the computer gets nothing of the assistant's (machineTrust is not nil).
func (s *Service) machinePending(ctx context.Context, machine string) bool {
	return s.machineTrust(ctx, machine) != nil
}

// machineTrust is nil for a confirmed computer, errMachinePending for one waiting to be confirmed. A list that could
// never be read gives errTrustUnknown for every computer (keys are not handed out on a guess); after one good read a
// failed one keeps the last.
func (s *Service) machineTrust(ctx context.Context, machine string) error {
	st := s.trustSrc
	if st == nil {
		return nil
	}
	b := &s.trust
	b.mu.Lock()
	if !b.stale() {
		defer b.mu.Unlock()
		return b.verdict(machine)
	}
	b.mu.Unlock()

	b.load.Lock()
	defer b.load.Unlock()
	b.mu.Lock()
	if !b.stale() { // another read went through while this one waited
		defer b.mu.Unlock()
		return b.verdict(machine)
	}
	gen := b.gen
	b.mu.Unlock()
	list, err := st.PendingMachineIDs(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	if err != nil {
		s.logf("assistant: pending computers: %v", err)
		b.failedAt = time.Now()
		return b.verdict(machine)
	}
	b.pending = append(b.pending[:0], list...)
	b.ok, b.failedAt = true, time.Time{}
	if gen == b.gen {
		b.at = time.Now()
	}
	return b.verdict(machine)
}

// stale: the list is asked for again (b.mu held).
func (b *trustBook) stale() bool {
	if !b.failedAt.IsZero() && time.Since(b.failedAt) < trustRetry {
		return false
	}
	return b.at.IsZero() || time.Since(b.at) > trustFor
}

// verdict is machineTrust's answer from what was read (b.mu held).
func (b *trustBook) verdict(machine string) error {
	if !b.ok {
		return errTrustUnknown
	}
	if slices.Contains(b.pending, machine) {
		return errMachinePending
	}
	return nil
}

// forgetPendingRead forgets the last read of the computers waiting to be confirmed: it is asked again next time. A new
// handshake does so (bus.go onHello): a read that failed before the hub's hand-over (the manager connected its own list
// first: devices ownlist.go) is not kept for the connections the hand-over sets up again.
func (s *Service) forgetPendingRead() {
	b := &s.trust
	b.mu.Lock()
	b.at, b.failedAt = time.Time{}, time.Time{}
	b.gen++
	b.mu.Unlock()
}

// trustChanged forgets the last read (the hub's machines changed) and gives the machines the hello lists now and did
// not before (a computer just confirmed) the managed launchers.
func (s *Service) trustChanged(ctx context.Context, before, now []string) {
	s.forgetPendingRead()
	var added []string
	for _, id := range now {
		if !slices.Contains(before, id) {
			added = append(added, id)
		}
	}
	if len(added) == 0 {
		return
	}
	go func() { // off the hub's event loop: a node's answer can take seconds
		ctx := context.WithoutCancel(ctx)
		for _, id := range added {
			s.pushManaged(ctx, id)
		}
	}()
}
