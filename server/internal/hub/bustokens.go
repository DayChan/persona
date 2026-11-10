package hub

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// The bus's identities and their tokens (bus.go, migration 0311): one token per service, only its sha256 kept. Every
// identity is a manager (the assistant) for now; the stored role leaves room for others. Tokens start with jb_, so they
// are never mistaken for a device token (jd_), and neither opens what the other does.

const busTokenPrefix = "jb_"

var busManagerID = lazyre.New(`^[a-z][a-z0-9-]{0,31}$`)

// busStore keeps the identities (internal/db/bus.go on Postgres). Optional like peerStore: a hub whose store lacks
// it accepts no bus connections.
type busStore interface {
	SaveBusIdentity(ctx context.Context, b db.BusIdentity, tokenHash string) error
	BusIdentityByHash(ctx context.Context, hash string) (*db.BusIdentity, error)
	BusIdentities(ctx context.Context) ([]db.BusIdentity, error)
	DeleteBusIdentity(ctx context.Context, id string) (bool, error)
}

var _ busStore = (*db.DB)(nil)

func (h *Hub) busStore() busStore {
	if h.bus.idents != nil {
		return h.bus.idents
	}
	s, _ := h.store.(busStore)
	return s
}

// createBusToken makes the token of manager id and returns it; it is shown once and never stored. An identity that
// had a token gets a new one: the old one stops working and a connection that came with it is hung up; its name stays
// (name is the new one's).
func (h *Hub) createBusToken(ctx context.Context, id, name string) (string, error) {
	s := h.busStore()
	if s == nil {
		return "", errors.New("no database")
	}
	if !busManagerID.MatchString(id) {
		return "", fmt.Errorf("bad manager id %q", id)
	}
	tok := busTokenPrefix + randomHex(24)
	name = clip(strings.TrimSpace(name), 80)
	if ms, ok := s.(managerStore); ok {
		// The name the user gave it stays (the hub makes the assistant a new token every start: managers.go).
		if b, err := ms.BusIdentityByID(ctx, id); err == nil && b != nil && b.Name != "" {
			name = b.Name
		}
	}
	ident := db.BusIdentity{ID: id, Role: bus.RoleManager, Name: name}
	if err := s.SaveBusIdentity(ctx, ident, hashToken(tok)); err != nil {
		return "", err
	}
	h.ownersChanged() // the roster kept (ownership.go) has it now
	h.busHangUp(id, bus.CloseRevoked, "its token was replaced")
	return tok, nil
}

// revokeBusToken removes identity id and hangs up its connections. False: there was none.
func (h *Hub) revokeBusToken(ctx context.Context, id string) (bool, error) {
	s := h.busStore()
	if s == nil {
		return false, errors.New("no database")
	}
	ok, err := s.DeleteBusIdentity(ctx, id)
	if err != nil {
		return false, err
	}
	h.ownersChanged()      // the roster kept (ownership.go) no longer has it
	h.extRoster.forget(id) // not in the roster any more, from now on (extensions_proof.go)
	h.busHangUp(id, bus.CloseRevoked, "its token was revoked")
	return ok, nil
}

// busIdentity is the identity bus token tok names, if any. Looked up afresh every time: a service dials rarely, and
// a revoked token stops at once. err is the store failing (no database, or it did not answer): then nobody can say
// whether the token is good, and the caller must not answer as if it were not.
func (h *Hub) busIdentity(ctx context.Context, tok string) (db.BusIdentity, bool, error) {
	if !strings.HasPrefix(tok, busTokenPrefix) {
		return db.BusIdentity{}, false, nil
	}
	s := h.busStore()
	if s == nil {
		return db.BusIdentity{}, false, errors.New("no database")
	}
	b, err := s.BusIdentityByHash(ctx, hashToken(tok))
	if err != nil {
		return db.BusIdentity{}, false, err
	}
	if b == nil {
		return db.BusIdentity{}, false, nil
	}
	return *b, true, nil
}
