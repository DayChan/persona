package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
)

type fakeBridge struct {
	mu       sync.Mutex
	sessions []AgentSession
	sends    []string // clientIds
	sendErr  error
	reads    []string
	listN    int
	avatars  map[string]string
}

func (f *fakeBridge) AgentBridgeStatus(context.Context) (*AgentBridgeState, error) {
	return &AgentBridgeState{Enabled: true}, nil
}
func (f *fakeBridge) AgentBridgeFeed(context.Context) (*AgentFeed, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	feed := &AgentFeed{Enabled: true}
	for k, v := range f.avatars {
		b, _ := json.Marshal(map[string]string{"feedId": k, "avatarUrl": v})
		feed.Items = append(feed.Items, b)
	}
	return feed, nil
}
func (f *fakeBridge) AgentSessions(context.Context) ([]AgentSession, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listN++
	return append([]AgentSession(nil), f.sessions...), nil
}
func (f *fakeBridge) AgentSessionSearch(context.Context, string) ([]AgentSearchHit, error) {
	return []AgentSearchHit{{SessionKey: "k"}}, nil
}
func (f *fakeBridge) AgentMessages(_ context.Context, key, cursor string, _ int) (*AgentMessagePage, error) {
	return &AgentMessagePage{Session: &AgentSession{SessionKey: key}, NextCursor: cursor + "x"}, nil
}
func (f *fakeBridge) AgentSend(_ context.Context, _, clientID, _ string) (json.RawMessage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sends = append(f.sends, clientID)
	return nil, f.sendErr
}
func (f *fakeBridge) AgentDeleteMessage(context.Context, string, string) error { return nil }
func (f *fakeBridge) AgentMarkRead(_ context.Context, key string) error {
	f.reads = append(f.reads, key)
	return nil
}

type memKV struct{ m map[string][]byte }

func (k *memKV) Setting(_ context.Context, key string, out any) (bool, error) {
	b, ok := k.m[key]
	if !ok {
		return false, nil
	}
	return true, json.Unmarshal(b, out)
}
func (k *memKV) PutSetting(_ context.Context, key string, v any) error {
	b, err := json.Marshal(v)
	k.m[key] = b
	return err
}

const key = "agent:grok_bot:a:8f79/"
const id = "lark-bridge:" + key

func newSvc(f *fakeBridge, kv KV) (*Service, *[][]string) {
	var changes [][]string
	var mu sync.Mutex
	s := NewService(kv, func(ids []string) { mu.Lock(); changes = append(changes, ids); mu.Unlock() }, nil, testBridge{API: f})
	return s, &changes
}

func status(err error) int { return HTTPStatus(err) }

func TestSendIdempotentByClientID(t *testing.T) {
	FollowUps = nil
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, CanSend: true}}}
	kv := &memKV{m: map[string][]byte{}}
	s, _ := newSvc(f, kv)
	ctx := context.Background()
	cid := NewClientID()
	for i := 0; i < 3; i++ {
		r, err := s.Send(ctx, id, cid, "hi", "app")
		if err != nil {
			t.Fatal(err)
		}
		if r.Duplicate != (i > 0) || r.Entry.Status != OutSent {
			t.Fatalf("send %d: %+v", i, r)
		}
	}
	if len(f.sends) != 1 {
		t.Fatalf("source got %d sends", len(f.sends))
	}
	// the outbox survives a restart (persisted in the settings store)
	s2, _ := newSvc(f, kv)
	if r, err := s2.Send(ctx, id, strings.ToUpper(cid), "hi", "app"); err != nil || !r.Duplicate {
		t.Fatalf("after restart: %+v %v", r, err)
	}
	if len(f.sends) != 1 {
		t.Fatal("resent after restart")
	}
}

func TestSendRetriesFailedWithSameClientID(t *testing.T) {
	FollowUps = nil
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, CanSend: true}}, sendErr: errors.New("down")}
	s, _ := newSvc(f, nil)
	ctx := context.Background()
	if _, err := s.Send(ctx, id, "", "hi", "app"); err == nil {
		t.Fatal("no error")
	}
	cid := f.sends[0]
	if e := s.Outbox(ctx, id, 0); len(e) != 1 || e[0].Status != OutFailed {
		t.Fatalf("outbox %+v", e)
	}
	f.sendErr = nil
	r, err := s.Send(ctx, id, cid, "hi", "app")
	if err != nil || r.Duplicate || r.Entry.Attempts != 2 || f.sends[1] != cid {
		t.Fatalf("retry %+v %v %v", r, err, f.sends)
	}
}

func TestSendRefusals(t *testing.T) {
	FollowUps = nil
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, CanSend: false}, {SessionKey: "ok", CanSend: true}}}
	s, _ := newSvc(f, nil)
	ctx := context.Background()
	cases := []struct {
		id, cid, text string
		want          int
	}{
		{id, "", "hi", http.StatusConflict}, // canSend=false
		{"lark-bridge:ok", "", strings.Repeat("a", 20001), http.StatusRequestEntityTooLarge},
		{"lark-bridge:ok", "", strings.Repeat("😀", 10001), http.StatusRequestEntityTooLarge}, // 20002 UTF-16 units
		{"lark-bridge:ok", "", "  ", http.StatusBadRequest},
		{"lark-bridge:ok", "nope", "hi", http.StatusBadRequest},
		{"lark-bridge:missing", "", "hi", http.StatusNotFound},
		{"other:ok", "", "hi", http.StatusNotFound},
		{"bad", "", "hi", http.StatusBadRequest},
	}
	for _, c := range cases {
		if _, err := s.Send(ctx, c.id, c.cid, c.text, ""); status(err) != c.want {
			t.Errorf("%s %q: got %v (%d), want %d", c.id, c.text[:min(len(c.text), 5)], err, status(err), c.want)
		}
	}
	if len(f.sends) != 0 {
		t.Fatalf("source called %d times", len(f.sends))
	}
	if _, err := s.Send(ctx, "lark-bridge:ok", "", strings.Repeat("a", 20000), ""); err != nil {
		t.Fatalf("20000 chars refused: %v", err)
	}
	cid := f.sends[0]
	if _, err := s.Send(ctx, id, cid, "hi", ""); status(err) != http.StatusConflict {
		t.Fatalf("clientId reused across sessions: %v", err)
	}
}

// A source's refusal keeps its status (a plugin's codes are mapped to *Error); anything else is a bad gateway.
func TestHTTPStatus(t *testing.T) {
	if HTTPStatus(&Error{Status: 404}) != 404 || HTTPStatus(errors.New("x")) != 502 {
		t.Fatal("mapping")
	}
}

func TestPollReportsChangedSessionsOnly(t *testing.T) {
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 1}, {SessionKey: "b", RankTimeMs: 1}}}
	s, changes := newSvc(f, nil)
	ctx := context.Background()
	s.PollOnce(ctx)
	if len(*changes) != 0 {
		t.Fatalf("baseline reported %v", *changes)
	}
	f.sessions[1].UnreadCount = 3
	f.sessions = append(f.sessions, AgentSession{SessionKey: "c"})
	s.PollOnce(ctx)
	if len(*changes) != 1 || strings.Join((*changes)[0], ",") != "lark-bridge:b,lark-bridge:c" {
		t.Fatalf("changes %v", *changes)
	}
}

// lateSvc is a service with no sources, as the manager builds it (every source comes later through Add), and the
// session ids of each change it reports.
func lateSvc() (*Service, func() [][]string) {
	var mu sync.Mutex
	var changes [][]string
	s := NewService(nil, func(ids []string) { mu.Lock(); changes = append(changes, ids); mu.Unlock() }, nil)
	return s, func() [][]string { mu.Lock(); defer mu.Unlock(); return slices.Clone(changes) }
}

// A source added after the first poll gets a baseline of its own: what it lists on its first poll is not reported
// (Add already told the apps), what moves after that is. One whose first list fails is baselined when it answers.
func TestPollBaselinesASourceAddedLater(t *testing.T) {
	s, changes := lateSvc()
	ctx := context.Background()
	s.PollOnce(ctx)
	ext := &memSource{name: "ext", sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 1}, {SessionKey: "b", RankTimeMs: 1}}}
	s.Add(ext)
	s.PollOnce(ctx)
	if got := changes(); len(got) != 0 {
		t.Fatalf("a late source's first poll reported %v", got)
	}
	ext.sessions[1].UnreadCount = 2
	s.PollOnce(ctx)
	if got := changes(); len(got) != 1 || strings.Join(got[0], ",") != "ext:b" {
		t.Fatalf("changes %v", got)
	}

	down := &memSource{name: "down", fail: errors.New("not running"), sessions: []AgentSession{{SessionKey: "x"}}}
	s.Add(down)
	s.PollOnce(ctx)
	down.fail = nil
	s.PollOnce(ctx)
	if got := changes(); len(got) != 1 {
		t.Fatalf("a source's first answer after a failed list reported %v", got[1:])
	}
}

// A source removed and added again (its plugin restarted) starts over: no burst of every session it lists.
func TestPollReaddedSourceStartsOver(t *testing.T) {
	s, changes := lateSvc()
	ctx := context.Background()
	s.Add(&memSource{name: "ext", sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 1}}})
	s.PollOnce(ctx)
	s.Remove("ext")
	n := len(changes()) // Remove tells the apps itself
	again := &memSource{name: "ext", sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 5}, {SessionKey: "b", RankTimeMs: 1}}}
	s.Add(again)
	s.PollOnce(ctx)
	if got := changes(); len(got) != n {
		t.Fatalf("the source added again reported %v", got[n:])
	}
	again.sessions = append(again.sessions, AgentSession{SessionKey: "c"})
	s.PollOnce(ctx)
	if got := changes(); len(got) != n+1 || strings.Join(got[n], ",") != "ext:c" {
		t.Fatalf("changes %v", got[n:])
	}

	// Restarted while the poll lists it: the old one's late answer is neither reported nor the new one's baseline.
	fresh := &memSource{name: "ext", sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 9}}}
	again.sessions[0].RankTimeMs = 6
	again.during = func() { s.Remove("ext"); s.Add(fresh) }
	s.PollOnce(ctx)
	s.PollOnce(ctx)
	for _, ids := range changes()[n+1:] {
		if len(ids) != 0 {
			t.Fatalf("a source restarted mid-list reported %v", ids)
		}
	}
}

// Polls racing sources being added and removed never report a session: none of them ever changes.
func TestPollRacingAddAndRemove(t *testing.T) {
	s, changes := lateSvc()
	ctx := context.Background()
	var wg sync.WaitGroup
	for g := range 4 {
		wg.Add(2)
		go func() {
			defer wg.Done()
			for range 50 {
				s.PollOnce(ctx)
			}
		}()
		go func() {
			defer wg.Done()
			name := fmt.Sprintf("ext%d", g%2)
			for range 50 {
				s.Add(&memSource{name: name, sessions: []AgentSession{{SessionKey: "a", RankTimeMs: 1}, {SessionKey: "b", UnreadCount: 1}}})
				s.PollOnce(ctx)
				s.Remove(name)
			}
		}()
	}
	wg.Wait()
	for _, ids := range changes() {
		if len(ids) != 0 {
			t.Fatalf("an unchanged session was reported: %v", ids)
		}
	}
}

func TestCacheInvalidatedByUpdate(t *testing.T) {
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, CanSend: true}}}
	s, changes := newSvc(f, nil)
	ctx := context.Background()
	for i := 0; i < 2; i++ {
		if l, err := s.Sessions(ctx, "", "grok_bot"); err != nil || len(l) != 0 { // provider unset in fake
			t.Fatal(l, err)
		}
	}
	if f.listN != 1 {
		t.Fatalf("listed %d times, want cached", f.listN)
	}
	s.Updated("lark-bridge", []string{key})
	if _, _ = s.Sessions(ctx, "", ""); f.listN != 2 {
		t.Fatalf("not refreshed after update (%d)", f.listN)
	}
	if got := (*changes)[0]; len(got) != 1 || got[0] != id {
		t.Fatalf("change ids %v", got)
	}
	if err := s.MarkRead(ctx, id); err != nil || f.reads[0] != key {
		t.Fatalf("read %v %v", err, f.reads)
	}
	p, err := s.Messages(ctx, id, "c1", 10)
	if err != nil || p.NextCursor != "c1x" || p.Session.ID != id {
		t.Fatalf("page %+v %v", p, err)
	}
}

func TestPrefsPersist(t *testing.T) {
	kv := &memKV{m: map[string][]byte{}}
	s, _ := newSvc(&fakeBridge{}, kv)
	if _, err := s.PutPrefs(context.Background(), Prefs{Pins: []string{"x"}, View: View{Sort: "name"}}); err != nil {
		t.Fatal(err)
	}
	s2, _ := newSvc(&fakeBridge{}, kv)
	p, err := s2.Prefs(context.Background())
	if err != nil || len(p.Pins) != 1 || p.Pins[0] != "x" || p.View.Sort != "name" {
		t.Fatalf("%v %+v", err, p)
	}
}

// failingKV fails its reads while down (the database restarting, a request cancelled); writes go through.
type failingKV struct {
	memKV
	down bool
}

func (k *failingKV) Setting(ctx context.Context, key string, out any) (bool, error) {
	if k.down {
		return false, errors.New("database unavailable")
	}
	return k.memKV.Setting(ctx, key, out)
}

// The outbox's first read failing used to mark it read with nothing in it: the next send wrote that one row over the
// stored outbox (up to 500 rows), and a retry of a clientId sent before went to the source again.
func TestOutboxFailedReadKeepsWhatIsStored(t *testing.T) {
	FollowUps = nil
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, CanSend: true}}}
	kv := &failingKV{memKV: memKV{m: map[string][]byte{}}}
	ctx := context.Background()
	s, _ := newSvc(f, kv)
	sent := NewClientID()
	if _, err := s.Send(ctx, id, sent, "示例", "app"); err != nil {
		t.Fatal(err)
	}

	s2, _ := newSvc(f, kv) // the hub started again, its first read of the outbox fails
	kv.down = true
	if _, err := s2.Send(ctx, id, NewClientID(), "示例二", "app"); err != nil {
		t.Fatal(err)
	}
	kv.down = false
	if _, err := s2.Send(ctx, id, NewClientID(), "示例三", "app"); err != nil {
		t.Fatal(err)
	}
	var stored []OutboxEntry
	_, _ = kv.memKV.Setting(ctx, "agents.outbox", &stored)
	if !slices.ContainsFunc(stored, func(e OutboxEntry) bool { return e.ClientID == sent }) {
		t.Fatalf("the row sent before the failed read is gone from the stored outbox: %+v", stored)
	}
	if r, err := s2.Send(ctx, id, sent, "示例", "app"); err != nil || !r.Duplicate {
		t.Fatalf("a retry of a clientId sent before: %+v %v", r, err)
	}
	if len(f.sends) != 3 {
		t.Fatalf("the source got %d sends, want 3: %v", len(f.sends), f.sends)
	}
}
