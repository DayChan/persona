package node

import (
	"bytes"
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// sentList is what the manager holds of this node's session list, as far as the node can tell: the params of the
// last sessions.changed it wrote. A list with exactly those bytes is not written again (#2439): a Codex app-server
// that keeps failing made notifySessions send the same 67 KB list every 5 s, which the manager decoded and dropped.
//
// Whatever may leave the manager holding something else makes it forget: a sessions.revs (it moves revs in its
// copy), a sessions.list answer (most callers take it as the list, the hub's relay does not: unknown either way),
// an engine switch (its list is pushed once, as before), another connection (another peer) and a failed write. The
// next list after any of them goes out as it always did.
type sentList struct {
	mu   sync.Mutex // held while encoding and writing: the wire has the lists in the order last was set
	peer *rpc.Peer  // the connection last went to
	last []byte     // nil: not known
	at   uint64     // epoch when last was written
	// scratch is where the next list is encoded to be compared with last; a list that goes swaps the two, so a list
	// the manager holds already is encoded and dropped without allocating (#2826).
	scratch []byte

	// epoch moves after anything written outside mu that the manager applies to its list (an answer, a switch);
	// pending counts the sessions.list answers being built or not yet written: no list is taken as held meanwhile.
	epoch   atomic.Uint64
	pending atomic.Int32
}

// sendSessions writes list as sessions.changed unless the manager holds these very bytes already. The wire bytes are
// the ones Notify always wrote for {"sessions": list}.
func (n *Node) sendSessions(list []proto.Session) {
	peer := n.peer
	if peer == nil {
		return
	}
	s := &n.sent
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := appendSessionsParams(s.scratch[:0], list)
	if err != nil { // nothing goes, as from Notify
		s.last = nil
		return
	}
	s.scratch = b // kept for the next list, unless this one goes and becomes last
	// pending before epoch, never the other way round: the skip is decided as of the pending read. An answer that
	// starts after it lists the sessions later than list was built, so the manager may well take it. One that ends
	// between the two reads has moved the epoch read below. Read epoch first and an answer could start and end in
	// between unseen, with list skipped behind it on the wire.
	idle := s.pending.Load() == 0
	epoch := s.epoch.Load()
	if s.last != nil && s.peer == peer && s.at == epoch && idle && bytes.Equal(s.last, b) {
		return
	}
	if err := peer.Notify("sessions.changed", rpc.Marshaled(b)); err != nil { // json.Marshal's bytes (appendSessionsParams)
		s.last = nil
		return
	}
	// Kept as of epoch: an answer still pending blocks the skip above until it ends, and then the epoch has moved, as
	// it has for an answer or a switch that went while this was written (it may be after it on the wire). Notify has
	// copied b onto the wire by now, so it is ours to keep; the old last becomes the next list's scratch.
	s.peer, s.at = peer, epoch
	s.last, s.scratch = b, s.last[:0]
	if cap(s.scratch) > 1<<20 { // a rare huge list's buffer is not kept twice
		s.scratch = nil
	}
}

// appendSessionsParams appends to b what json.Marshal gives for {"sessions": list}: each session written by
// proto.AppendSession, a nil list as null. A session AppendSession leaves to encoding/json has the whole list encoded
// by json.Marshal, its bytes and its error both.
func appendSessionsParams(b []byte, list []proto.Session) ([]byte, error) {
	if list == nil {
		return append(b, `{"sessions":null}`...), nil
	}
	b = append(b, `{"sessions":[`...)
	for i := range list {
		if i > 0 {
			b = append(b, ',')
		}
		var ok bool
		if b, ok = proto.AppendSession(b, &list[i]); !ok {
			return json.Marshal(map[string]any{"sessions": list})
		}
	}
	return append(b, "]}"...), nil
}

// sendRevs writes revs as sessions.revs; the manager's list is not the last one written any more.
func (n *Node) sendRevs(revs map[string]int64) {
	if n.peer == nil {
		return
	}
	s := &n.sent
	s.mu.Lock()
	defer s.mu.Unlock()
	s.last = nil
	_ = n.peer.Notify("sessions.revs", map[string]any{"revs": revs})
}

// forget: the next list goes out whatever the last one was (an engine switch).
func (s *sentList) forget() { s.epoch.Add(1) }

// answering marks a sessions.list answer of the request ctx until it is written: rpc ends a request's ctx after its
// answer is on the wire (Peer.handle), or when the request or the connection is cancelled, when the manager takes no
// answer. A ctx that never ends (tests calling handle directly) keeps every later list going out, as before.
func (s *sentList) answering(ctx context.Context) {
	s.pending.Add(1)
	context.AfterFunc(ctx, func() {
		s.epoch.Add(1) // before the count drops: a list compared after it sees the new epoch
		s.pending.Add(-1)
	})
}
