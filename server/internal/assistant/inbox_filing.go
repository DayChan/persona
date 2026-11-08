package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"slices"
	"time"
)

// Filing exposes its done marker while saves queue, so concurrent filing/reopening requests retain their order.
// Closing handling is staged separately: neither a delivery pass nor another save may see its stop note until the
// row commits. A failed row therefore never changes the live suggestion, draft, pending notes or delivery count.
type inboxFiling struct {
	it            *InboxItem
	previous      doneState
	applied       doneState
	close         bool
	now           time.Time
	why           string
	staged        []byte
	beforeClosure []byte
	agent         *InboxAgent
	handling      *InboxHandling
	suggestion    *InboxSuggestion
	told          int
	// A request can replace another request's optimistic marker while that row is still being written. If both
	// fail, rollback must reach the state before either request, rather than restoring the first failed marker.
	previousWrite *inboxFiling
	settled       bool
	prepared      bool
	failed        bool
	closed        bool
}

type doneState struct {
	DoneAt   *time.Time
	DoneBy   string
	chain    []InboxChainEntry
	chainSeq int
}

func doneStateOf(it *InboxItem) doneState {
	return doneState{it.DoneAt, it.DoneBy, slices.Clone(it.Chain), it.ChainSeq}
}

func (b *inboxBook) beginFiling(it *InboxItem, now time.Time, why string) *inboxFiling {
	w := &inboxFiling{it: it, previous: doneStateOf(it), now: now, why: why,
		beforeClosure: closureState(it), agent: it.Agent, handling: it.Handling, suggestion: it.Suggestion}
	if it.Agent != nil {
		w.told = it.Agent.told
	}
	if pending := b.filing[it.ID]; pending != nil && b.holdsFiling(pending) {
		w.previousWrite = pending
	}
	return w
}

func (b *inboxBook) settleFiling(w *inboxFiling) {
	if b.filing == nil {
		b.filing = map[string]*inboxFiling{}
	}
	w.applied = doneStateOf(w.it)
	b.touched(w.it.ID)
	b.filing[w.it.ID] = w
}

func (b *inboxBook) holdsFiling(w *inboxFiling) bool {
	it := w.it
	return b.items[it.ID] == it && b.filing[it.ID] == w && it.DoneAt == w.applied.DoneAt && it.DoneBy == w.applied.DoneBy
}

// filingSnapshot removes optimistic changes belonging to other pending requests. An unrelated save (such as a
// delivery acknowledgement) must not make a filing durable before that request reaches its own write. A filing
// writes its own marker/chain, leaving later requests queued; a newer committed request supersedes older ones.
func (b *inboxBook) filingSnapshot(it *InboxItem, target *inboxFiling) (*InboxItem, bool) {
	latest := b.filing[it.ID]
	if latest == nil || !b.holdsFiling(latest) {
		return it, false
	}
	c := *it
	for w := latest; w != nil; w = w.previousWrite {
		if w == target {
			for p := w.previousWrite; p != nil && (!p.settled || p.failed); p = p.previousWrite {
				restoreFiling(&c, p, false)
			}
			return &c, true
		}
		if w.settled && !w.failed {
			break
		}
		restoreFiling(&c, w, true)
	}
	return &c, false
}

// closureState excludes unrelated fields: a concurrent title or parent edit must not prevent a committed closure.
func closureState(it *InboxItem) []byte {
	agent := it.Agent
	if agent != nil {
		a := *agent
		// An in-flight delivery may trim notes and finish a wake while this row is out. Its own save follows ours;
		// it must not prevent closure or clear the newly committed stop note. New notes are detected by told.
		a.Pending, a.Wake, a.StartedAt = nil, false, nil
		agent = &a
	}
	raw, _ := json.Marshal(struct {
		Agent      *InboxAgent
		Handling   *InboxHandling
		Suggestion *InboxSuggestion
	}{agent, it.Handling, it.Suggestion})
	return raw
}

// prepareClosing detaches every record closeHandling mutates. The nested draft/notes are only read or detached by
// closeHandling, so their existing aliases remain intact on the live item. Under mu.
func (b *inboxBook) prepareClosing(w *inboxFiling) *InboxItem {
	it := w.it
	row, prepared := b.filingSnapshot(it, w)
	w.prepared = prepared
	if !w.close || !prepared {
		return row
	}
	if w.previousWrite != nil && w.previousWrite.closed && !it.handled() {
		w.closed = true
		return row // its earlier committed closure is already queued; keep that note for delivery
	}
	if !w.sameHandling(w.beforeClosure) {
		return row // handling changed while this filing was waiting for its save turn
	}
	w.staged = w.beforeClosure
	c := *row
	if it.Agent != nil {
		a := *it.Agent
		c.Agent = &a
	}
	if it.Handling != nil {
		h := *it.Handling
		c.Handling = &h
	}
	b.closeHandling(&c, w.why, w.now)
	return &c
}

func (w *inboxFiling) sameHandling(state []byte) bool {
	return w.it.Agent == w.agent && w.it.Handling == w.handling && w.it.Suggestion == w.suggestion &&
		(w.agent == nil || w.agent.told == w.told) && bytes.Equal(closureState(w.it), state)
}

// restoreFiling restores only this filing's done/chain edits. Keep entries and edits that arrived while storage was
// out, including a newer chain sequence; reconstruct a capped chain from the previous entries and those kept now.
func restoreFiling(it *InboxItem, w *inboxFiling, owned bool) {
	prior := w
	for prior.previousWrite != nil && prior.previousWrite.failed {
		prior = prior.previousWrite
	}
	prev := prior.previous
	// An intervening successful request can separate failed writers. Its captured previous chain still contains
	// those older optimistic entries, so normalize it before restoring it; otherwise rollback resurrects them.
	chain := InboxItem{Chain: prev.chain, ChainSeq: prev.chainSeq}
	for p := prior.previousWrite; p != nil; p = p.previousWrite {
		if p.failed {
			restoreFilingChain(&chain, p, p.previous)
		}
	}
	prev.chain, prev.chainSeq = chain.Chain, chain.ChainSeq
	if owned {
		it.DoneAt, it.DoneBy = prev.DoneAt, prev.DoneBy
	}
	restoreFilingChain(it, w, prev)
}

func restoreFilingChain(it *InboxItem, w *inboxFiling, prev doneState) {
	old := make(map[string]bool, len(prev.chain))
	for _, e := range prev.chain {
		old[e.ID] = true
	}
	added := map[string]bool{}
	for _, e := range w.applied.chain {
		if !old[e.ID] {
			added[e.ID] = true
		}
	}
	current := map[string]InboxChainEntry{}
	for _, e := range it.Chain {
		current[e.ID] = e
	}
	chain := slices.Clone(prev.chain)
	for i, e := range chain {
		if c, ok := current[e.ID]; ok {
			chain[i] = c
		}
	}
	for _, e := range it.Chain {
		if !old[e.ID] && !added[e.ID] {
			chain = append(chain, e)
		}
	}
	it.Chain = chain[max(0, len(chain)-chainMax):]
	if it.ChainSeq == w.applied.chainSeq {
		it.ChainSeq = prev.chainSeq
	}
}

func (s *Service) saveInboxFilings(ctx context.Context, ws []*inboxFiling) map[string]bool {
	b := &s.inbox
	items := make([]*InboxItem, 0, len(ws))
	byID := make(map[string]*inboxFiling, len(ws))
	for _, w := range ws {
		items = append(items, w.it)
		byID[w.it.ID] = w
	}
	var tell []*InboxItem
	failed := s.saveInboxPrepared(ctx, items, func(it *InboxItem) *InboxItem {
		return b.prepareClosing(byID[it.ID])
	}, func(failed map[string]bool) {
		for _, w := range ws {
			pending := false
			for p := b.filing[w.it.ID]; p != nil && (!p.settled || p.failed); p = p.previousWrite {
				if p == w {
					pending = true
					break
				}
			}
			w.settled, w.failed = true, failed[w.it.ID]
			owned := b.holdsFiling(w)
			if b.filing[w.it.ID] == w {
				delete(b.filing, w.it.ID)
			}
			if b.items[w.it.ID] != w.it {
				continue
			}
			if failed[w.it.ID] {
				// A newer filing owns the done marker, but must not keep the failed request's chain entries.
				if pending {
					restoreFiling(w.it, w, owned)
					if owned {
						// Saves can reach storage out of mutation order. Rolling back the latest writer restores an
						// older pending marker; keep its ownership so filters and later saves still see that chain.
						for p := w.previousWrite; p != nil; p = p.previousWrite {
							if p.failed {
								continue
							}
							if !p.settled && w.it.DoneAt == p.applied.DoneAt && w.it.DoneBy == p.applied.DoneBy {
								b.filing[w.it.ID] = p
							}
							break
						}
					}
				}
			} else if w.prepared {
				for p := w.previousWrite; p != nil && (!p.settled || p.failed); p = p.previousWrite {
					restoreFiling(w.it, p, false)
				}
			}
			if !w.failed && w.prepared && w.staged != nil && w.sameHandling(w.staged) {
				// A concurrent handling update owns its next save. Publish only the closure actually stored, on the
				// same conversation; a replaced agent or a new suggestion must not be stopped by this older row.
				if b.closeHandling(w.it, w.why, w.now) {
					tell = append(tell, w.it)
				}
				w.closed = true
			}
		}
	})
	for _, it := range tell {
		s.tellSoon(ctx, it)
	}
	return failed
}
