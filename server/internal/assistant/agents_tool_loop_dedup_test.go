package assistant

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

func loopSettingPuts(store *workMemStore) int {
	store.mu.Lock()
	defer store.mu.Unlock()
	return store.puts
}

func TestToolLoopStableStateDoesNotWriteAgain(t *testing.T) {
	for _, reason := range []loopClearReason{loopCandidateGone, loopUserInput} {
		t.Run(map[loopClearReason]string{loopCandidateGone: "candidate gone", loopUserInput: "queued input"}[reason], func(t *testing.T) {
			s, f, store, c, _ := persistentLoopFixture(t)
			k := [2]string{"box", "s1"}
			if reason == loopUserInput {
				c.Queued = []string{"next instruction"}
			} else {
				c.Status, c.ToolLoop = proto.StatusDone, nil
			}
			f.setSessions("box", c)
			s.checkToolLoops(context.Background())
			s.work.mu.Lock()
			updated := s.work.loops[k].updatedAt
			s.work.mu.Unlock()
			before := loopSettingPuts(store)
			for range 3 {
				s.checkToolLoops(context.Background())
			}
			s.work.mu.Lock()
			got := s.work.loops[k].updatedAt
			s.work.mu.Unlock()
			if !updated.Equal(got) || loopSettingPuts(store) != before {
				t.Fatalf("unchanged state was refreshed or written: updated %v -> %v, writes %d -> %d", updated, got, before, loopSettingPuts(store))
			}
		})
	}
}

func TestToolLoopFailedClearRetriesAndSurvivesRestart(t *testing.T) {
	s, f, store, c, _ := persistentLoopFixture(t)
	store.mu.Lock()
	store.failPut = errors.New("write unavailable")
	store.mu.Unlock()
	s.clearToolLoop([2]string{"box", "s1"}, loopCandidateGone)
	store.mu.Lock()
	store.failPut = nil
	store.mu.Unlock()
	c.Status, c.ToolLoop = proto.StatusDone, nil
	f.setSessions("box", c)
	s.checkToolLoops(context.Background())
	restarted := newServiceOn(t, f, store)
	if err := restarted.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if cards := restarted.workCards(); len(cards) != 0 {
		t.Fatalf("failed clear was not retried: %+v", cards)
	}
}

func TestToolLoopSnapshotDedupKeepsUncommittedCard(t *testing.T) {
	s, _, store, _, card := persistentLoopFixture(t)
	ctx := context.Background()
	k := [2]string{"box", "s1"}
	s.work.mu.Lock()
	st := s.work.loops[k]
	st.answering, st.consuming, st.silenced = true, true, true
	s.work.mu.Unlock()
	before := loopSettingPuts(store)
	if err := s.saveToolLoops(ctx); err != nil {
		t.Fatal(err)
	}
	if loopSettingPuts(store) != before {
		t.Fatal("an unrelated save rewrote the unchanged pending decision")
	}
	var rows []savedToolLoop
	if _, err := store.Setting(ctx, toolLoopsSetting, &rows); err != nil || len(rows) != 1 || rows[0].Card == nil || rows[0].Card.ID != card.ID {
		t.Fatalf("uncommitted card changed: %+v, %v", rows, err)
	}
	if err := s.saveToolLoops(ctx, k); err != nil {
		t.Fatal(err)
	}
	rows = nil
	if _, err := store.Setting(ctx, toolLoopsSetting, &rows); err != nil || len(rows) != 1 || rows[0].Card != nil || !rows[0].Silenced {
		t.Fatalf("committed decision not saved: %+v, %v", rows, err)
	}
}

func TestToolLoopDisableBatchReplacesDedupSnapshot(t *testing.T) {
	s, _, store, _, _ := persistentLoopFixture(t)
	ctx := context.Background()
	if err := s.putTriageSwitches(ctx, TriageSwitches{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	before := loopSettingPuts(store)
	if err := s.saveToolLoops(ctx); err != nil {
		t.Fatal(err)
	}
	if loopSettingPuts(store) != before {
		t.Fatal("snapshot already saved in the switch batch was rewritten")
	}
}

func TestToolLoopSnapshotDedupConcurrentChanges(t *testing.T) {
	_, f, base, _, _ := persistentLoopFixture(t)
	store := &blockingLoopStore{workMemStore: base, entered: make(chan struct{}), release: make(chan struct{})}
	s := newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	k := [2]string{"box", "s1"}
	s.work.mu.Lock()
	s.work.loops[k].checks = 3
	s.work.mu.Unlock()
	store.block.Store(true)
	first := make(chan error, 1)
	go func() { first <- s.saveToolLoops(context.Background()) }()
	select {
	case <-store.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("first save did not start")
	}
	s.work.mu.Lock()
	s.work.loops[k].checks = 4
	s.work.mu.Unlock()
	second := make(chan error, 1)
	go func() { second <- s.saveToolLoops(context.Background()) }()
	close(store.release)
	for _, done := range []chan error{first, second} {
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("save did not finish")
		}
	}
	var rows []savedToolLoop
	if _, err := store.Setting(context.Background(), toolLoopsSetting, &rows); err != nil || len(rows) != 1 || rows[0].Checks != 4 {
		t.Fatalf("newer snapshot was lost: %+v, %v", rows, err)
	}
	before := loopSettingPuts(base)
	if err := s.saveToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loopSettingPuts(base) != before {
		t.Fatal("completed concurrent snapshot was written again")
	}
}

func TestToolLoopSnapshotDedupWithUninitializedTimestamp(t *testing.T) {
	s, _, store, _, _ := persistentLoopFixture(t)
	s.work.mu.Lock()
	s.work.loops[[2]string{"box", "s2"}] = &toolLoopState{checks: 1}
	s.work.mu.Unlock()
	if err := s.saveToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := loopSettingPuts(store)
	if err := s.saveToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if loopSettingPuts(store) != before {
		t.Fatal("unchanged state without an initial timestamp was rewritten")
	}
}
