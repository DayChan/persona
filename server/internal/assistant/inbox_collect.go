package assistant

import (
	"context"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// larkRequestAccount sets a Feishu item's OLAccount for the lark.account the hub runs with now.
func (b *inboxBook) larkRequestAccount(it *InboxItem) {
	if it.Lark == nil {
		return
	}
	it.Lark.OLAccount = 0
	if a := larkItemAccount(it); a != max(b.larkAccount, 1) {
		it.Lark.OLAccount = a
	}
}

// larkItemAccount is the gateway account a Feishu item came from; zero in rows from before accounts were recorded
// is account 1.
func larkItemAccount(it *InboxItem) int {
	if it.Lark == nil {
		return 0
	}
	return max(it.Lark.AccountID, 1)
}

// InboxCollected is inbox.collected: what one collection of the hub read. It applies what changed; the items a
// plugin's connector starts enter the list once triaged (admitInbox, in the background): until then nothing lists,
// stores or works on them. A round older than the last one applied of the hub's run is dropped (it was held up on the
// way); while what the inbox keeps can't be read the round is refused (errInboxUnavailable: the hub hands it again).
func (s *Service) InboxCollected(ctx context.Context, r assistantwire.InboxRound) error {
	if err := s.inboxReady(ctx); err != nil {
		return err
	}
	b := &s.inbox
	b.running.Lock()
	defer b.running.Unlock()
	if r.Seq > 0 {
		if r.Epoch == b.epoch && r.Seq <= b.seq {
			s.logf("inbox: round %d is older than %d, which was applied: dropped", r.Seq, b.seq)
			return nil
		}
		b.epoch, b.seq = r.Epoch, r.Seq
	}
	cfg := s.inboxConfig(ctx)
	b.knowExtensions(cfg.Extensions)
	b.srcMu.Lock()
	for id, name := range r.Names {
		b.names[id] = name
	}
	for _, src := range r.Sources {
		b.names[src.ID] = src.Name
		b.builtin[src.ID] = !src.Extension
		if src.Extension {
			b.extensions[src.ID] = true
		}
		for kind, d := range src.Kinds {
			b.kinds[src.ID+"/"+kind] = d
		}
	}
	b.srcMu.Unlock()
	b.mu.Lock()
	b.larkAccount = cfg.LarkAccount
	b.batch = newInboxBatch()
	b.mu.Unlock()
	var changed []*InboxItem
	// Mail (the hub's own), then the connectors in the order the hub has them.
	if r.Mail != nil {
		changed = append(changed, s.collectMail(ctx, r.Mail)...)
	}
	for _, src := range r.Sources {
		if src.ReadError != "" && len(src.Things) == 0 {
			continue // not read, and nothing held for it: what it had stays, and so does its held-back count
		}
		// A failed source may still carry what an earlier round read while the manager was away (the hub's held round,
		// mergeRounds): those things are applied; the failure is noted below either way.
		changed = append(changed, s.collectSource(ctx, src)...)
	}
	b.mu.Lock()
	readsChanged := b.recordReads(r)
	batch := b.batch
	batch.registered = r.Names
	// Done past inboxForget leaves memory; its row stays as history until purgeInboxHistory (below) deletes it.
	for id, it := range b.items {
		if it.DoneAt != nil && time.Since(*it.DoneAt) > inboxForget {
			b.drop(id)
		}
	}
	now := time.Now()
	// The round's last step files and tiers what it changed: those writes are taken up again (roundWrite), and the
	// mutes' filings join them. A row that fails is undone to what it holds, and the sources bring it again (#91).
	for _, it := range changed {
		b.roundWrite(it)
	}
	changed = append(changed, b.roundMuted(b.applyMutes(now))...)
	changed, todo := b.sortBatch(batch, changed, now)
	b.applyRuledPolicies(changed)
	b.settleRound()
	ws := b.roundSaved(changed)
	b.batch = nil
	b.mu.Unlock()
	s.saveInboxUndo(ctx, changed, s.undoWrites(ws, "what the round changed"))
	if st := s.inboxDB(); st != nil {
		s.purgeInboxHistory(ctx, st, now)
	}
	if readsChanged && len(changed) == 0 {
		s.inboxChanged(ctx, nil) // nothing came in, but which sources could be read did (saveInbox tells otherwise)
	}
	s.triageInbox(ctx)
	s.translateInbox(ctx)
	s.admitInbox(ctx, batch, todo)
	return nil
}

// recordReads keeps which of a round's sources could not be read, under b.mu: a source read now (or off) is cleared,
// one that failed is marked, one the hub no longer has goes, the others keep theirs. It says whether that changed.
func (b *inboxBook) recordReads(r assistantwire.InboxRound) bool {
	next := map[string]string{}
	for id, code := range b.readErrs {
		if _, there := r.Names[id]; r.Names == nil || there {
			next[id] = code
		}
	}
	for _, src := range r.Sources {
		if src.ReadError != "" {
			next[src.ID] = src.ReadError
		} else {
			delete(next, src.ID)
		}
	}
	changed := len(next) != len(b.readErrs)
	for id, code := range next {
		changed = changed || b.readErrs[id] != code
	}
	b.readErrs = next
	return changed
}

// ---- the connectors' ----

// collectSource applies one connector's things: its batches go to be related to events (in the background), a
// migrated connector's things with extra keep the items' own blocks, the others become generic items; then the open
// items its rules now leave out are filed.
func (s *Service) collectSource(ctx context.Context, src assistantwire.InboxSource) []*InboxItem {
	b := &s.inbox
	b.readSource(src.ID, src.Name)
	if len(src.Things) == 0 && !src.Settles {
		return nil
	}
	// Batches of new messages go to the model, not the list.
	things := s.relateBatches(ctx, src, src.Things)
	var own []connector.InboxThing // a migrated connector's things that keep the inbox's own item shape
	if src.Migrated {
		generic := things[:0:0]
		for _, t := range things {
			if t.Extra.HasBlock() { // only rule_tier (GitHub's, #2830): a generic item like any connector's
				own = append(own, t)
			} else {
				generic = append(generic, t)
			}
		}
		things = generic
	}
	var changed []*InboxItem
	b.mu.Lock()
	changed = append(changed, b.applyConnector(src.ID, src.Name, things, time.Now())...)
	changed = append(changed, b.applyMigrated(src.ID, own, time.Now())...)
	b.settleRound()
	b.mu.Unlock()
	if src.Settles {
		changed = append(changed, s.settleConnector(ctx, src.ID)...)
	}
	return changed
}
