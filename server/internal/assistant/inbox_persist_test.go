package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// inboxOrderStore delays the first write so an undo can race with it.
type inboxOrderStore struct {
	*inboxMemStore
	mu            sync.Mutex
	calls         atomic.Int32
	firstEntered  chan struct{}
	releaseFirst  chan struct{}
	secondSaved   chan struct{}
	deleteEntered chan struct{}
	deleteOnce    sync.Once
	saved         []byte
	done          *time.Time
}

func (s *inboxOrderStore) PutInboxItem(ctx context.Context, id string, data []byte, updatedAt time.Time, done *time.Time) error {
	n := s.calls.Add(1)
	if n == 1 && s.firstEntered != nil {
		close(s.firstEntered)
		<-s.releaseFirst
	}
	s.mu.Lock()
	s.saved = append([]byte(nil), data...)
	s.done = done
	s.mu.Unlock()
	if err := s.inboxMemStore.PutInboxItem(ctx, id, data, updatedAt, done); err != nil {
		return err
	}
	if n == 2 && s.secondSaved != nil {
		close(s.secondSaved)
	}
	return nil
}

func (s *inboxOrderStore) InboxItems(ctx context.Context, doneSince time.Time, each func(raw []byte) error) error {
	return s.inboxMemStore.InboxItems(ctx, doneSince, each)
}

func (s *inboxOrderStore) DeleteInboxSource(ctx context.Context, source string, ids []string) ([]string, error) {
	if s.deleteEntered != nil {
		s.deleteOnce.Do(func() { close(s.deleteEntered) })
	}
	return s.inboxMemStore.DeleteInboxSource(ctx, source, ids)
}

// inboxCutStore's start read breaks off after the first row.
type inboxCutStore struct{ *inboxMemStore }

func (s inboxCutStore) InboxItems(ctx context.Context, doneSince time.Time, each func(raw []byte) error) error {
	n := 0
	return s.inboxMemStore.InboxItems(ctx, doneSince, func(raw []byte) error {
		if n++; n > 1 {
			return errMuteStoreDown
		}
		return each(raw)
	})
}

// A start read that breaks off midway takes in nothing (the rows decoded before it are dropped) and is read again the
// next time, as one that fails at once.
func TestInboxLoadBrokenMidwayTakesNothing(t *testing.T) {
	st := newInboxMemStore()
	st.rows["a"], st.rows["b"] = []byte(`{"id":"a","source":"mail"}`), []byte(`{"id":"b","source":"mail"}`)
	h := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return inboxCutStore{st} }, Logf: t.Logf})
	if err := h.inboxReady(context.Background()); !errors.Is(err, errInboxUnavailable) || h.inbox.loaded.Load() || len(h.inbox.items) != 0 {
		t.Fatalf("a read broken after one row: %v, loaded %v, %d items in memory", err, h.inbox.loaded.Load(), len(h.inbox.items))
	}
}

func (s *inboxOrderStore) hasRow(id string) bool {
	s.imu.Lock()
	defer s.imu.Unlock()
	_, ok := s.rows[id]
	return ok
}

func TestInboxPersistenceKeepsLatestState(t *testing.T) {
	s := &inboxOrderStore{
		inboxMemStore: newInboxMemStore(),
		firstEntered:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondSaved:   make(chan struct{}),
	}
	h := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return s }, Logf: t.Logf})
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	h.inbox.items["item"] = &InboxItem{ID: "item", Source: "mail", UpdatedAt: time.Now()}
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(s.releaseFirst) }) }
	defer release()

	firstDone := make(chan error, 1)
	go func() {
		_, err := h.setInboxDone(context.Background(), "item", true, false)
		firstDone <- err
	}()
	select {
	case <-s.firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first save did not reach the store")
	}
	secondDone := make(chan error, 1)
	go func() {
		_, err := h.setInboxDone(context.Background(), "item", false, false)
		secondDone <- err
	}()
	// A serialized save waits here; without serialization the undo writes first.
	select {
	case <-s.secondSaved:
	case <-time.After(100 * time.Millisecond):
	}
	release()
	for _, done := range []<-chan error{firstDone, secondDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("an inbox save did not finish")
		}
	}

	s.mu.Lock()
	var stored InboxItem
	err := json.Unmarshal(s.saved, &stored)
	done := s.done
	s.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	if stored.DoneAt != nil || stored.DoneBy != "" || done != nil {
		t.Fatalf("the completed save overwrote undo: %+v, done column %v", stored, done)
	}
	restarted := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return s }, Logf: t.Logf})
	if err := restarted.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	restored := restarted.inbox.items["item"]
	if restored == nil || !restored.open() || restored.DoneBy != "" {
		t.Fatalf("undo was lost after restart: %+v", restored)
	}
}

func TestInboxForgetSerializesWithPersistence(t *testing.T) {
	s := &inboxOrderStore{
		inboxMemStore: newInboxMemStore(),
		firstEntered:  make(chan struct{}),
		releaseFirst:  make(chan struct{}),
		secondSaved:   make(chan struct{}),
		deleteEntered: make(chan struct{}),
	}
	h := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return s }, Logf: t.Logf})
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := &InboxItem{ID: "item", Source: "mail", UpdatedAt: time.Now()}
	h.inbox.mu.Lock()
	h.inbox.items[item.ID] = item
	h.inbox.mu.Unlock()
	var releaseOnce sync.Once
	release := func() { releaseOnce.Do(func() { close(s.releaseFirst) }) }
	defer release()

	firstDone := make(chan error, 1)
	go func() {
		_, err := h.setInboxDone(context.Background(), item.ID, true, false)
		firstDone <- err
	}()
	select {
	case <-s.firstEntered:
	case <-time.After(5 * time.Second):
		t.Fatal("the first save did not reach the store")
	}
	forgetDone := make(chan error, 1)
	go func() {
		_, err := h.InboxForget(context.Background(), item.Source)
		forgetDone <- err
	}()
	deletedBeforeSave := false
	select {
	case <-s.deleteEntered:
		deletedBeforeSave = true
	case <-time.After(100 * time.Millisecond):
	}
	release()
	for _, done := range []<-chan error{firstDone, forgetDone} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("an inbox persistence operation did not finish")
		}
	}
	if deletedBeforeSave {
		t.Fatal("InboxForget deleted the row while an older save was still in progress")
	}
	if s.hasRow(item.ID) {
		t.Fatal("the completed snapshot resurrected a forgotten item")
	}
}

// inboxForgetStore fails deletes on demand, then recovers, so a forget can be retried.
type inboxForgetStore struct {
	*inboxMemStore
	deleteFail error
	calls      atomic.Int32
}

func (s *inboxForgetStore) DeleteInboxSource(ctx context.Context, source string, ids []string) ([]string, error) {
	s.calls.Add(1)
	if s.deleteFail != nil {
		return nil, s.deleteFail
	}
	return s.inboxMemStore.DeleteInboxSource(ctx, source, ids)
}

// InboxForget deletes the rows before dropping items from memory: a failed delete keeps everything in place, and the
// contracted retry finishes the deletion instead of reporting success with the rows still stored.
func TestInboxForgetPersistsBeforeMemoryDelete(t *testing.T) {
	st := &inboxForgetStore{inboxMemStore: newInboxMemStore(), deleteFail: errMuteStoreDown}
	f := newFakeHub().withMachine()
	in := newFakeInbox()
	f.inbox = in
	h := New(Options{Hub: f, Store: func() Store { return st }, Logf: t.Logf})
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	items := []*InboxItem{
		{ID: "mail:1", Source: "mail", UpdatedAt: time.Now()},
		{ID: "mail:2", Source: "mail", UpdatedAt: time.Now()},
	}
	h.inbox.mu.Lock()
	for _, it := range items {
		h.inbox.items[it.ID] = it
		raw, _ := json.Marshal(it)
		if err := st.PutInboxItem(context.Background(), it.ID, raw, it.UpdatedAt, nil); err != nil {
			t.Fatal(err)
		}
	}
	h.inbox.mu.Unlock()

	if n, err := h.InboxForget(context.Background(), "mail"); !errors.Is(err, errMuteStoreDown) || n != 0 {
		t.Fatalf("forget with the store down: n=%d err=%v", n, err)
	}
	h.inbox.mu.Lock()
	for _, it := range items {
		if h.inbox.items[it.ID] != it {
			t.Fatalf("item %s left memory after a failed delete", it.ID)
		}
	}
	h.inbox.mu.Unlock()
	st.imu.Lock()
	rowsAfterFail := len(st.rows)
	st.imu.Unlock()
	if rowsAfterFail != 2 {
		t.Fatalf("a failed delete left %d rows, want 2", rowsAfterFail)
	}
	if pushes := in.pushes(); pushes != 0 {
		t.Fatalf("a failed forget told the apps %d times, want 0", pushes)
	}

	st.deleteFail = nil
	if n, err := h.InboxForget(context.Background(), "mail"); err != nil || n != 2 {
		t.Fatalf("retry: n=%d err=%v", n, err)
	}
	h.inbox.mu.Lock()
	left := len(h.inbox.items)
	h.inbox.mu.Unlock()
	st.imu.Lock()
	rowsAfterRetry := len(st.rows)
	st.imu.Unlock()
	if left != 0 || rowsAfterRetry != 0 {
		t.Fatalf("after the retry: %d items in memory, %d rows stored", left, rowsAfterRetry)
	}
	if pushes := in.pushes(); pushes != 1 {
		t.Fatalf("the successful forget told the apps %d times, want 1", pushes)
	}
	if st.calls.Load() != 2 {
		t.Fatalf("the store saw %d delete calls, want 2", st.calls.Load())
	}
}

func TestSaveInboxSkipsForgottenItem(t *testing.T) {
	s := &inboxOrderStore{inboxMemStore: newInboxMemStore()}
	h := New(Options{Hub: newFakeHub().withMachine(), Store: func() Store { return s }, Logf: t.Logf})
	if err := h.inboxReady(context.Background()); err != nil {
		t.Fatal(err)
	}
	item := &InboxItem{ID: "item", Source: "mail", UpdatedAt: time.Now()}
	h.inbox.mu.Lock()
	h.inbox.items[item.ID] = item
	h.inbox.mu.Unlock()
	if _, err := h.InboxForget(context.Background(), item.Source); err != nil {
		t.Fatal(err)
	}
	// This is the pointer a save that was queued before Forget would carry.
	h.saveInbox(context.Background(), []*InboxItem{item})
	if s.hasRow(item.ID) {
		t.Fatal("a queued save resurrected an item removed by InboxForget")
	}
}
