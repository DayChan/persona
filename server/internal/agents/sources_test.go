package agents

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"github.com/nwverse/persona/server/internal/sessionlist"
)

// memSource is an agents.Source held in memory, as a plugin's would be.
type memSource struct {
	name     string
	sessions []AgentSession
	listN    int
	fail     error
	during   func() // runs while Sessions is being answered
}

func (m *memSource) Name() string { return m.name }
func (m *memSource) Status(context.Context) (Status, error) {
	return Status{Source: m.name, Enabled: true}, nil
}
func (m *memSource) Sessions(context.Context) ([]Session, error) {
	m.listN++
	if m.during != nil {
		m.during()
	}
	if m.fail != nil {
		return nil, m.fail
	}
	out := make([]Session, len(m.sessions))
	for i, s := range m.sessions {
		out[i] = Wrap(m.name, s, "")
	}
	return out, nil
}
func (m *memSource) Search(context.Context, string) ([]SearchHit, error) { return nil, m.fail }
func (m *memSource) Messages(context.Context, string, string, int) (*MessagePage, error) {
	return &MessagePage{Messages: []Message{}}, nil
}
func (m *memSource) Send(context.Context, string, string, string) (json.RawMessage, error) {
	return json.RawMessage(`{}`), nil
}
func (m *memSource) DeleteMessage(context.Context, string, string) error { return nil }
func (m *memSource) MarkRead(context.Context, string) error              { return nil }

// A source added while the hub runs is listed after the hub's own and reached by its ids; one by a taken name is
// refused; a removed one is gone with what was cached of it, and the apps are told.
func TestAddAndRemoveSources(t *testing.T) {
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, Provider: "grok_bot", CanSend: true}}}
	s, changes := newSvc(f, nil)
	ctx := context.Background()
	plug := &memSource{name: "grokbot", sessions: []AgentSession{{SessionKey: "s1", Provider: "grok_bot", CanSend: true}}}

	if !s.Add(plug) {
		t.Fatal("a new source is added")
	}
	if s.Add(&memSource{name: testSource}) || s.Add(&memSource{name: "grokbot"}) {
		t.Fatal("a name that is taken is refused")
	}
	if st := s.Statuses(ctx); len(st) != 2 || st[0].Source != testSource || st[1].Source != "grokbot" {
		t.Fatalf("statuses %+v", st)
	}
	list, err := s.Sessions(ctx, "", "grok_bot")
	if err != nil || len(list) != 2 || list[1].ID != "grokbot:s1" || list[1].Source != "grokbot" {
		t.Fatalf("sessions %+v %v", list, err)
	}
	if _, err := s.Send(ctx, "grokbot:s1", "", "hi", "test"); err != nil {
		t.Fatalf("send to the added source: %v", err)
	}
	if _, err := s.Messages(ctx, "grokbot:s1", "", 20); err != nil {
		t.Fatal(err)
	}

	n := len(*changes)
	s.Remove("grokbot")
	if len(*changes) != n+1 {
		t.Fatal("the apps are told when a source goes")
	}
	if list, _ := s.Sessions(ctx, "", ""); len(list) != 1 || list[0].Source != testSource {
		t.Fatalf("after remove %+v", list)
	}
	if _, ok := s.cache.getSessions("grokbot"); ok {
		t.Fatal("its cached list is dropped")
	}
	if _, ok := s.cache.getLatest("grokbot:s1", 20); ok {
		t.Fatal("its cached pages are dropped")
	}
	if _, err := s.Messages(ctx, "grokbot:s1", "", 20); status(err) != http.StatusNotFound {
		t.Fatalf("a removed source's session: %v", err)
	}
	s.Remove("grokbot") // twice is nothing
	if !s.Add(plug) {
		t.Fatal("added again after it went")
	}
}

// A source that fails (its plugin crashed, timed out) is left out of the lists of all of them so the others are still
// there; asked for by name its error is the answer, and so it is for a lone source that has listed before.
func TestFailingSourceDoesNotHideOthers(t *testing.T) {
	f := &fakeBridge{sessions: []AgentSession{{SessionKey: key, Provider: "grok_bot", CanSend: true}}}
	s, _ := newSvc(f, nil)
	ctx := context.Background()
	down := errors.New("plugin not running")
	s.Add(&memSource{name: "broken", fail: down})
	if list, err := s.Sessions(ctx, "", ""); err != nil || len(list) != 1 || list[0].Source != testSource {
		t.Fatalf("the others' sessions: %+v %v", list, err)
	}
	if _, err := s.Search(ctx, "", "x"); err != nil {
		t.Fatalf("the others' search: %v", err)
	}
	if _, err := s.Sessions(ctx, "broken", ""); !errors.Is(err, down) {
		t.Fatalf("asked by name: %v", err)
	}
	if st := s.Statuses(ctx); st[1].Error != down.Error() {
		t.Fatalf("its status says why: %+v", st[1])
	}
}

// A lone source that has never listed (an extension waiting for its settings) does not fail the lists of all of them,
// and its status says why. Once it has listed, a failure is an outage: the error is the answer and its rows stay.
func TestLoneSourceFailure(t *testing.T) {
	s, changes := newSvc(&fakeBridge{}, nil)
	s.Remove(testSource)
	ctx := context.Background()
	notSet := errors.New("not configured yet")
	lone := &memSource{name: "lone", fail: notSet, sessions: []AgentSession{{SessionKey: "s1", CanSend: true}}}
	s.Add(lone)

	opts := sessionlist.Options{Limit: 50}
	if list, err := s.Sessions(ctx, "", ""); err != nil || len(list) != 0 {
		t.Fatalf("listing all: %+v %v", list, err)
	}
	if hits, err := s.Search(ctx, "", "x"); err != nil || len(hits) != 0 {
		t.Fatalf("searching all: %+v %v", hits, err)
	}
	if page, err := s.ListPage(ctx, opts); err != nil || len(page.Sessions) != 0 {
		t.Fatalf("paged: %+v %v", page, err)
	}
	if counts, err := s.ListCounts(ctx, opts); err != nil || counts.Total != 0 {
		t.Fatalf("counts: %+v %v", counts, err)
	}
	if _, err := s.Sessions(ctx, "lone", ""); !errors.Is(err, notSet) {
		t.Fatalf("asked by name: %v", err)
	}
	if _, err := s.Search(ctx, "lone", "x"); !errors.Is(err, notSet) {
		t.Fatalf("searched by name: %v", err)
	}
	if _, err := s.ListPage(ctx, sessionlist.Options{Limit: 50, Source: "lone"}); !errors.Is(err, notSet) {
		t.Fatalf("paged by name: %v", err)
	}
	if st := s.Statuses(ctx); st[0].Error != notSet.Error() {
		t.Fatalf("status: %+v", st[0])
	}

	lone.fail = nil
	n := len(*changes)
	if page, err := s.ListPage(ctx, opts); err != nil || len(page.Sessions) != 1 {
		t.Fatalf("set up: %+v %v", page, err)
	}
	if len(*changes) != n+1 {
		t.Fatal("the apps are told it is back")
	}
	if st := s.Statuses(ctx); st[0].Error != "" {
		t.Fatalf("status after it answered: %+v", st[0])
	}

	lone.fail = context.Canceled
	s.cache.invalidate("lone", nil)
	_, _ = s.Sessions(ctx, "", "")
	if st := s.Statuses(ctx); st[0].Error != "" {
		t.Fatalf("a request that went away is not the source's failure: %+v", st[0])
	}

	down := errors.New("gateway unreachable")
	lone.fail = down
	s.cache.invalidate("lone", nil)
	if _, err := s.ListPage(ctx, opts); !errors.Is(err, down) {
		t.Fatalf("an outage is the answer: %v", err)
	}
	if _, err := s.ListCounts(ctx, opts); !errors.Is(err, down) {
		t.Fatalf("counts in an outage: %v", err)
	}
	lone.fail = nil
	if page, err := s.ListPage(ctx, opts); err != nil || len(page.Sessions) != 1 {
		t.Fatalf("rows kept through the outage: %+v %v", page, err)
	}

	s.Remove("lone")
	lone.fail = notSet
	s.Add(lone)
	if _, err := s.Sessions(ctx, "", ""); err != nil {
		t.Fatalf("added again, it has not listed: %v", err)
	}

	// what an old registration answers late says nothing of the one added since
	stale := &memSource{name: "lone"}
	s.Remove("lone")
	s.Add(stale)
	s.noteListing(lone, notSet)
	if st := s.Statuses(ctx); st[0].Error != "" {
		t.Fatalf("a late failure of the old registration: %+v", st[0])
	}
}

// What a removed source answers late is not cached: a source by that name added again (its plugin upgraded) is asked
// itself. A send's follow-up for a removed source tells the apps nothing.
func TestRemovedSourceAnswerNotCached(t *testing.T) {
	s, changes := newSvc(&fakeBridge{}, nil)
	ctx := context.Background()
	old := &memSource{name: "grokbot", sessions: []AgentSession{{SessionKey: "old"}}}
	old.during = func() { s.Remove("grokbot") } // removed while its list is being answered
	s.Add(old)
	if _, err := s.Sessions(ctx, "grokbot", ""); err != nil {
		t.Fatal(err)
	}
	fresh := &memSource{name: "grokbot", sessions: []AgentSession{{SessionKey: "new"}}}
	s.Add(fresh)
	list, err := s.Sessions(ctx, "grokbot", "")
	if err != nil || len(list) != 1 || list[0].SessionKey != "new" || fresh.listN != 1 {
		t.Fatalf("the old answer came back: %+v %v (listed %d)", list, err, fresh.listN)
	}
	s.Remove("grokbot")
	n := len(*changes)
	s.Updated("grokbot", []string{"new"})
	if len(*changes) != n {
		t.Fatal("an update for a removed source was pushed")
	}
}
