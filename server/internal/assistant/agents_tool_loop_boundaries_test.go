package assistant

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

func TestToolLoopMissingCandidateRequiresProgress(t *testing.T) {
	s, f, store, c, _ := persistentLoopFixture(t)
	c.Rev = 100
	f.setSessions("box", c)
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{c}})
	missing := c
	missing.ToolLoop, missing.Rev = nil, 120
	f.setSessions("box", missing)
	s.checkToolLoops(context.Background())
	if len(s.workCards()) != 0 {
		t.Fatal("advanced transcript left the old card up")
	}
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(s.workCards()) != 0 {
		t.Fatal("withdrawn card returned after restart")
	}
}

func TestToolLoopReconnectMissingCandidateDoesNotProveProgress(t *testing.T) {
	_, f, store, c, card := persistentLoopFixture(t)
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	missing := c
	missing.ToolLoop, missing.Rev = nil, 200
	f.setSessions("box", missing)
	s.checkToolLoops(context.Background())
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatalf("unconfirmed reconnect withdrew the card: %+v", cards)
	}
	// Once this connection confirms the same candidate, later transcript
	// progress with no candidate ends it.
	c.Rev = 220
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{c}})
	missing.Rev = 240
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	if len(s.workCards()) != 0 {
		t.Fatal("confirmed candidate did not clear after transcript progress")
	}
}

func TestToolLoopReconnectContinuedProgressWithdrawsStaleCard(t *testing.T) {
	_, f, store, c, card := persistentLoopFixture(t)
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	missing := c
	missing.ToolLoop, missing.Rev = nil, 200
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	missing.Rev = 210
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatal("single revision increase withdrew the card")
	}
	missing.Rev = 220
	s.toolLoopsChanged(DeviceSessions{MachineID: "box", Sessions: []proto.Session{missing}})
	if len(s.workCards()) != 0 {
		t.Fatal("continuing transcript progress left a stale card")
	}
	shifted := *c.ToolLoop
	shifted.Since = shifted.Since.Add(time.Second)
	c.ToolLoop, c.Rev = &shifted, 240
	s.considerToolLoop(context.Background(), "box", c)
	if len(s.workCards()) != 0 {
		t.Fatal("ambiguous rescan opened another card")
	}
}

func TestToolLoopReadFailureDoesNotHideOrdinaryWorkWhenOff(t *testing.T) {
	store := &workMemStore{}
	store.failKeys = map[string]error{toolLoopsSetting: errors.New("loop storage unavailable")}
	row := db.SessionWork{MachineID: "box", SessionID: "s1", TurnAt: time.Now().UTC(), Work: proto.WorkState{Phase: proto.WorkDone, Line: "Done"}}
	if err := store.PutSessionWork(context.Background(), row); err != nil {
		t.Fatal(err)
	}
	f := newFakeHub()
	for _, sw := range []TriageSwitches{{Enabled: true}, {}} {
		s := newServiceOn(t, f, store)
		s.work.switches = &sw
		report := s.workReport(context.Background())
		if report == nil || report.On != sw.Enabled || sw.Enabled && len(report.Sessions) != 1 {
			t.Fatalf("loop read failure hid ordinary work: %+v", report)
		}
	}
}

type failingLoopBatchStore struct {
	*workMemStore
	fail atomic.Bool
}

func (s *failingLoopBatchStore) PutSettings(ctx context.Context, values map[string]any) error {
	if s.fail.Load() {
		return errors.New("atomic settings write failed")
	}
	return s.workMemStore.memStore.PutSettings(ctx, values)
}

func TestToolLoopDisableWriteFailureKeepsSettingsAndCard(t *testing.T) {
	_, f, base, _, card := persistentLoopFixture(t)
	old := TriageSwitches{Enabled: true, ToolLoop: true, Wake: true}
	if err := base.PutSetting(context.Background(), triageSettingKey, old); err != nil {
		t.Fatal(err)
	}
	store := &failingLoopBatchStore{workMemStore: base}
	s := newServiceOn(t, f, store)
	s.work.switches = &old
	if err := s.loadToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	store.fail.Store(true)
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, Wake: true}); err == nil {
		t.Fatal("failed atomic write was acknowledged")
	}
	if got := s.triageSwitches(context.Background()); got != old {
		t.Fatalf("switches changed despite failure: %+v", got)
	}
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatalf("card changed despite failure: %+v", cards)
	}
	var saved TriageSwitches
	if ok, err := store.Setting(context.Background(), triageSettingKey, &saved); err != nil || !ok || saved != old {
		t.Fatalf("saved switches changed: %+v, %v", saved, err)
	}
	s = newServiceOn(t, f, store)
	if err := s.loadToolLoops(context.Background()); err != nil || len(s.workCards()) != 1 {
		t.Fatalf("saved card did not survive failed disable: %v", err)
	}
}

type failingLoopExpiryStore struct {
	*fleetMem
	puts, failAt atomic.Int32
	failHistory  atomic.Bool
}

func (s *failingLoopExpiryStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == toolLoopsSetting && s.puts.Add(1) == s.failAt.Load() {
		return errors.New("loop state write failed")
	}
	return s.fleetMem.PutSetting(ctx, key, value)
}

func (s *failingLoopExpiryStore) FinishInteraction(ctx context.Context, id, status string, r *proto.Resolution) error {
	if s.failHistory.CompareAndSwap(true, false) {
		return errors.New("history write failed")
	}
	return s.fleetMem.FinishInteraction(ctx, id, status, r)
}

func expiryLoopFixture(t *testing.T) (*Service, *fakeHub, *failingLoopExpiryStore, proto.Interaction) {
	t.Helper()
	store := &failingLoopExpiryStore{fleetMem: newFleetMem()}
	f := newFakeHub()
	c := proto.Session{SessionID: "s1", Status: proto.StatusWorking, ToolLoop: &proto.ToolLoop{Tool: "Read", Count: 30, Since: time.Now().Add(-time.Minute).UTC()}}
	f.setSessions("box", c)
	s := newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	st := &toolLoopState{tool: c.ToolLoop.Tool, since: c.ToolLoop.Since, pending: true}
	s.work.mu.Lock()
	s.work.init()
	s.work.loops[[2]string{"box", "s1"}] = st
	s.work.mu.Unlock()
	s.reviewToolLoop(context.Background(), [2]string{"box", "s1"}, st, *c.ToolLoop, true)
	card := s.workCards()[0]
	card.ExpiresAt = time.Now().Add(-time.Second)
	s.work.mu.Lock()
	s.work.cards[card.ID] = card
	s.work.mu.Unlock()
	if err := s.saveToolLoops(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := store.InsertInteraction(context.Background(), card); err != nil {
		t.Fatal(err)
	}
	return s, f, store, card
}

func TestToolLoopExpirySaveFailureRetriesEverything(t *testing.T) {
	s, f, store, card := expiryLoopFixture(t)
	store.failAt.Store(store.puts.Load() + 1) // unchanged tick snapshot is skipped; expiration save fails
	s.checkToolLoops(context.Background())
	if cards := s.workCards(); len(cards) != 1 || cards[0].ID != card.ID {
		t.Fatalf("failed expiry lost retryable card: %+v", cards)
	}
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "pending" {
		t.Fatalf("history changed despite failed save: %s", status)
	}
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	f.mu.Lock()
	before := len(f.snaps)
	f.mu.Unlock()
	s.checkToolLoops(context.Background())
	if len(s.workCards()) != 0 {
		t.Fatal("expired card not withdrawn after retry")
	}
	f.mu.Lock()
	published := len(f.snaps) > before
	f.mu.Unlock()
	if !published {
		t.Fatal("expiry retry did not publish the withdrawal")
	}
	for _, ix := range f.lastSnapshot().Interactions {
		if ix.ID == card.ID {
			t.Fatal("expiry retry published the old card")
		}
	}
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "expired" {
		t.Fatalf("history not expired after retry: %s", status)
	}
}

func TestToolLoopExpiryMarkerClearFailureRetries(t *testing.T) {
	s, f, store, card := expiryLoopFixture(t)
	store.failAt.Store(store.puts.Load() + 2) // expiration save succeeds; marker clear fails
	s.checkToolLoops(context.Background())
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "expired" {
		t.Fatalf("history did not expire: %s", status)
	}
	s.work.mu.Lock()
	state := s.work.loops[[2]string{"box", "s1"}]
	pending := state != nil && state.expiredID == card.ID
	s.work.mu.Unlock()
	if !pending {
		t.Fatal("failed marker clear was not retained for retry")
	}
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	s.checkToolLoops(context.Background())
	s.work.mu.Lock()
	state = s.work.loops[[2]string{"box", "s1"}]
	pending = state != nil && state.expiredID != ""
	s.work.mu.Unlock()
	if pending {
		t.Fatal("expired marker was not cleared after restart")
	}
}

func TestToolLoopExpiryHistorySurvivesNewTurn(t *testing.T) {
	s, f, store, card := expiryLoopFixture(t)
	store.failHistory.Store(true)
	s.checkToolLoops(context.Background())
	s.clearToolLoop([2]string{"box", "s1"}, loopTurnChanged)
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	s.checkToolLoops(context.Background())
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "expired" {
		t.Fatalf("new turn lost pending expiry: %s", status)
	}
}

type failingTriagePutStore struct {
	*failingLoopExpiryStore
	fail atomic.Bool
}

func (s *failingTriagePutStore) PutSetting(ctx context.Context, key string, value any) error {
	if key == triageSettingKey && s.fail.Load() {
		return errors.New("triage settings write failed")
	}
	return s.failingLoopExpiryStore.PutSetting(ctx, key, value)
}

func TestToolLoopExpirySurvivesDisabledSettingsUpdateAfterRestart(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		loaded, failPut, failRead bool
	}{
		{"unloaded", false, false, false},
		{"loaded", true, false, false},
		{"failed PUT", false, true, false},
		{"loaded failed PUT", true, true, false},
		{"loop read unavailable", false, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, f, base, card := expiryLoopFixture(t)
			base.failHistory.Store(true)
			s.checkToolLoops(context.Background())
			if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true}); err != nil {
				t.Fatal(err)
			}
			store := &failingTriagePutStore{failingLoopExpiryStore: base}
			s = newServiceOn(t, f, store)
			if tc.loaded {
				if err := s.loadToolLoops(context.Background()); err != nil {
					t.Fatal(err)
				}
			}
			if tc.failRead {
				store.mu.Lock()
				store.failKeys = map[string]error{toolLoopsSetting: errors.New("loop read unavailable")}
				store.mu.Unlock()
			}
			store.fail.Store(tc.failPut)
			err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true, Wake: true})
			if (err != nil) != tc.failPut {
				t.Fatalf("settings update error = %v", err)
			}
			store.fail.Store(false)
			store.mu.Lock()
			delete(store.failKeys, toolLoopsSetting)
			store.mu.Unlock()
			var rows []savedToolLoop
			if ok, err := store.Setting(context.Background(), toolLoopsSetting, &rows); err != nil || !ok || len(rows) != 1 || rows[0].ExpiredID != card.ID {
				t.Fatalf("pending expiry lost: %+v, %v", rows, err)
			}
			s.work.mu.Lock()
			loaded := s.work.loopLoaded
			s.work.mu.Unlock()
			if loaded != tc.loaded {
				t.Fatalf("loop loading changed: got %v, want %v", loaded, tc.loaded)
			}
			var saved TriageSwitches
			if ok, err := store.Setting(context.Background(), triageSettingKey, &saved); err != nil || !ok || saved.Wake == tc.failPut {
				t.Fatalf("unexpected saved settings: %+v, %v", saved, err)
			}
			s.checkToolLoops(context.Background())
			if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "expired" {
				t.Fatalf("pending expiry not completed: %s", status)
			}
		})
	}
}

func TestToolLoopExpiryHistorySurvivesDisabling(t *testing.T) {
	s, f, store, card := expiryLoopFixture(t)
	store.failHistory.Store(true)
	s.checkToolLoops(context.Background())
	if err := s.putTriageSwitches(context.Background(), TriageSwitches{Enabled: true}); err != nil {
		t.Fatal(err)
	}
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true}
	s.checkToolLoops(context.Background())
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "expired" {
		t.Fatalf("disabling lost pending expiry: %s", status)
	}
}

func TestToolLoopExpiryHistoryFailureSurvivesRestart(t *testing.T) {
	s, f, store, card := expiryLoopFixture(t)
	store.failHistory.Store(true)
	s.checkToolLoops(context.Background())
	if len(s.workCards()) != 0 {
		t.Fatal("saved expired card remained visible")
	}
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "pending" {
		t.Fatalf("failed history unexpectedly changed: %s", status)
	}
	s = newServiceOn(t, f, store)
	s.work.switches = &TriageSwitches{Enabled: true, ToolLoop: true}
	s.checkToolLoops(context.Background())
	if status, _, _ := store.InteractionOutcome(context.Background(), card.ID); status != "expired" {
		t.Fatalf("history not retried after restart: %s", status)
	}
}
