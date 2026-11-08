package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/rpc"
)

// inboxStore is the database's inbox (table inbox_items); a store without it keeps the inbox in memory.
type inboxStore interface {
	PutInboxItem(ctx context.Context, id string, data []byte, updatedAt time.Time, doneAt *time.Time) error
	InboxItems(ctx context.Context, doneSince time.Time) ([][]byte, error)
	DeleteInboxItems(ctx context.Context, ids []string) error
}

// inboxBatchStore is an inboxStore that writes many items in one round trip (the database's PutInboxItems).
type inboxBatchStore interface {
	PutInboxItems(ctx context.Context, rows []db.InboxRow) error
}

func (s *Service) inboxDB() inboxStore {
	st, _ := s.store().(inboxStore)
	return st
}

// errInboxUnavailable: what the inbox keeps could not be read (CodeInboxUnavailable).
var errInboxUnavailable = &rpc.Error{Code: assistantwire.CodeInboxUnavailable, Message: "收件箱暂时读不到存着的内容，稍后再试"}

// inboxReady loads what the inbox keeps (its items and mutes) the first time anything needs it. A load that failed
// (the database did not answer) is errInboxUnavailable and is tried again the next time: until one goes through
// nothing is taken or written, so the items and mutes stored are not overwritten with what memory has.
func (s *Service) inboxReady(ctx context.Context) error {
	b := &s.inbox
	b.init()
	if b.loaded.Load() {
		return nil
	}
	b.loadMu.Lock()
	defer b.loadMu.Unlock()
	if b.loaded.Load() {
		return nil
	}
	if !s.loadInbox(ctx) {
		return errInboxUnavailable
	}
	b.loaded.Store(true)
	return nil
}

// inboxUp is inboxReady for a route: false when it answered 503 inbox_unavailable.
func (s *Service) inboxUp(w http.ResponseWriter, r *http.Request) bool {
	if err := s.inboxReady(r.Context()); err != nil {
		apierr.WriteCode(w, http.StatusServiceUnavailable, assistantwire.CodeInboxUnavailable, err)
		return false
	}
	return true
}

// loadInbox reads the items still remembered and the mutes; false when the database could not be read.
func (s *Service) loadInbox(ctx context.Context) bool {
	b := &s.inbox
	if !s.loadInboxMutes(ctx) || !s.loadInboxPolicies(ctx) {
		return false
	}
	st := s.inboxDB()
	if st == nil {
		return true
	}
	rows, err := st.InboxItems(ctx, time.Now().Add(-inboxForget))
	if err != nil {
		s.logf("inbox: load: %v", err)
		return false
	}
	cfg := s.inboxConfig(ctx)
	b.knowExtensions(cfg.Extensions)
	b.mu.Lock()
	defer b.mu.Unlock()
	b.larkAccount = cfg.LarkAccount
	for _, raw := range rows {
		var it InboxItem
		if json.Unmarshal(raw, &it) == nil && it.ID != "" {
			if _, have := b.items[it.ID]; have {
				continue // newer in memory than in the database
			}
			b.larkRequestAccount(&it)
			if it.ArrivedOf == "" { // stored before arrivals were stamped
				it.ArrivedOf, it.ArrivedAt = it.Last, it.UpdatedAt
			}
			if len(it.Chain) == 0 { // stored before chains: received and triaged, from its fields
				it.Chain = synthesizedChain(b.userLang(), &it)
				it.ChainSeq = len(it.Chain)
			}
			b.items[it.ID] = &it
		}
	}
	return true
}

// saveInbox persists changed items and tells the hub (it tells the extensions and the apps). It returns the ids
// whose rows could not be written (the in-memory state of those is the caller's to roll back: with saveInboxUndo).
func (s *Service) saveInbox(ctx context.Context, changed []*InboxItem) map[string]bool {
	return s.saveInboxUndo(ctx, changed, nil)
}

// saveInboxUndo is saveInbox with the rollback of what could not be written: undo runs with the failed ids, b.mu
// held, before the next save may start. Undone after b.saving, a save queued behind the failed one (the store back
// by then) would write the change first and the undo would leave memory behind the row until a restart (#4924).
func (s *Service) saveInboxUndo(ctx context.Context, changed []*InboxItem, undo func(failed map[string]bool)) map[string]bool {
	return s.saveInboxPrepared(ctx, changed, nil, undo)
}

// saveInboxPrepared can stage a detached row and publish its committed effects under mu before saving is released.
// prepare runs under mu; finish runs there after the store answers, including when every row succeeded.
func (s *Service) saveInboxPrepared(ctx context.Context, changed []*InboxItem, prepare func(*InboxItem) *InboxItem, finish func(map[string]bool)) map[string]bool {
	b := &s.inbox
	if len(changed) == 0 {
		return nil
	}
	if !b.loaded.Load() { // not read yet: what is stored stays as it is (inboxReady)
		s.logf("inbox: %d changed items not saved: what is stored was not read", len(changed))
		return nil
	}
	var told []assistantwire.InboxChangedItem
	b.saving.Lock()
	b.mu.Lock()
	rows := make([]db.InboxRow, 0, len(changed))
	now := time.Now()
	for i, it := range changed {
		if current := b.items[it.ID]; current != it {
			continue // the item was forgotten or replaced while this save was waiting
		}
		it.arrived(now)
		row := it
		if prepare != nil {
			row = prepare(it)
		} else {
			row, _ = b.filingSnapshot(it, nil)
		}
		raw, _ := json.Marshal(row)
		rows = append(rows, db.InboxRow{ID: row.ID, Data: raw, UpdatedAt: row.UpdatedAt, DoneAt: row.DoneAt})
		if i < 50 { // a first round's hundreds: the plugin reads the inbox if it wants them all
			told = append(told, assistantwire.InboxChangedItem{ID: it.ID, Source: it.Source, Kind: it.Kind, Title: it.Title})
		}
	}
	b.mu.Unlock()
	if len(rows) == 0 {
		b.saving.Unlock()
		return nil
	}
	var failed map[string]bool
	if st := s.inboxDB(); st != nil {
		failed = s.putInboxRows(ctx, st, rows)
	}
	if finish != nil {
		b.mu.Lock()
		finish(failed)
		b.mu.Unlock()
	}
	b.saving.Unlock()
	if len(failed) > 0 {
		// What was not stored is not announced: the apps would otherwise drop items the database still holds open.
		told = slices.DeleteFunc(told, func(i assistantwire.InboxChangedItem) bool { return failed[i.ID] })
	}
	s.inboxChanged(ctx, told)
	return failed
}

// putInboxRows writes rows in one go when the store can (a round's hundreds are one round trip, not hundreds); when
// that fails, or the store can't, each row is written on its own so one that fails does not keep the others out. It
// returns the ids that were not written anywhere.
func (s *Service) putInboxRows(ctx context.Context, st inboxStore, rows []db.InboxRow) map[string]bool {
	if bs, ok := st.(inboxBatchStore); ok && len(rows) > 1 {
		err := bs.PutInboxItems(ctx, rows)
		if err == nil {
			return nil
		}
		s.logf("inbox: save %d items at once: %v (one by one)", len(rows), err)
	}
	var failed map[string]bool
	for _, r := range rows {
		if err := st.PutInboxItem(ctx, r.ID, r.Data, r.UpdatedAt, r.DoneAt); err != nil {
			s.logf("inbox: save %s: %v", r.ID, err)
			if failed == nil {
				failed = map[string]bool{}
			}
			failed[r.ID] = true
		}
	}
	return failed
}

// inboxChanged tells the hub what changed (EventInboxChanged): it tells the extensions and the apps.
func (s *Service) inboxChanged(ctx context.Context, items []assistantwire.InboxChangedItem) {
	if items == nil {
		items = []assistantwire.InboxChangedItem{}
	}
	s.hub.InboxChanged(ctx, assistantwire.InboxChanged{Items: items})
}
