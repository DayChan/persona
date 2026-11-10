package agents

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/sessionlist"
)

func TestPagedSourceFailureDropsStaleRows(t *testing.T) {
	a := &memSource{name: "first", sessions: []AgentSession{{SessionKey: "a"}}}
	b := &memSource{name: "second", sessions: []AgentSession{{SessionKey: "b"}}}
	s := NewService(nil, nil, nil, a, b)
	first, err := s.ListPage(context.Background(), sessionlist.Options{})
	if err != nil || first.Total != 2 {
		t.Fatalf("baseline: %+v %v", first, err)
	}
	b.fail = errors.New("demo source unavailable")
	s.Updated("second", nil)
	page, err := s.ListPage(context.Background(), sessionlist.Options{Since: first.Revision})
	if err != nil || page.Total != 1 || len(page.Remove) != 1 || page.Remove[0] != "second:b" {
		t.Fatalf("failed source: %+v %v", page, err)
	}
}

func TestPagedCanceledRequestKeepsIndexedRows(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	a := &memSource{name: "first", sessions: []AgentSession{{SessionKey: "a"}}}
	b := &memSource{name: "second", sessions: []AgentSession{{SessionKey: "b"}}}
	s := NewService(nil, nil, logf, a, b)
	first, err := s.ListPage(context.Background(), sessionlist.Options{})
	if err != nil || first.Total != 2 {
		t.Fatalf("baseline: %+v %v", first, err)
	}
	b.fail = context.Canceled
	s.cache.invalidate("second", nil)
	if _, err := s.ListPage(context.Background(), sessionlist.Options{Since: first.Revision}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ListPage: %v", err)
	}
	if _, err := s.ListCounts(context.Background(), sessionlist.Options{}); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled ListCounts: %v", err)
	}
	if _, err := s.Sessions(context.Background(), "", ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Sessions: %v", err)
	}
	if _, err := s.Search(context.Background(), "", "a"); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled Search: %v", err)
	}
	if len(logs) != 0 {
		t.Fatalf("canceled requests logged as source failures: %v", logs)
	}
	b.fail = nil
	page, err := s.ListPage(context.Background(), sessionlist.Options{Since: first.Revision})
	if err != nil || page.Total != 2 || len(page.Remove) != 0 || page.Revision != first.Revision {
		t.Fatalf("indexed rows dropped on cancel: %+v %v (was %s)", page, err, first.Revision)
	}
}

func TestReturnedSourceFailureIsLogged(t *testing.T) {
	var logs []string
	logf := func(format string, args ...any) { logs = append(logs, fmt.Sprintf(format, args...)) }
	lone := &memSource{name: "lone", sessions: []AgentSession{{SessionKey: "a"}}}
	s := NewService(nil, nil, logf, lone)
	if _, err := s.ListPage(context.Background(), sessionlist.Options{}); err != nil {
		t.Fatal(err)
	}
	down := errors.New("gateway unreachable")
	lone.fail = down
	s.cache.invalidate("lone", nil)
	if _, err := s.ListPage(context.Background(), sessionlist.Options{}); !errors.Is(err, down) {
		t.Fatalf("ListPage: %v", err)
	}
	if _, err := s.Sessions(context.Background(), "", ""); !errors.Is(err, down) {
		t.Fatalf("Sessions: %v", err)
	}
	if _, err := s.Search(context.Background(), "", "a"); !errors.Is(err, down) {
		t.Fatalf("Search: %v", err)
	}
	for _, want := range []string{
		"agents: lone: paginated sessions: gateway unreachable",
		"agents: lone: sessions: gateway unreachable",
		"agents: lone: search: gateway unreachable",
	} {
		if !strings.Contains(strings.Join(logs, "\n"), want) {
			t.Fatalf("missing log %q in %v", want, logs)
		}
	}
}

func TestPagedLateInvalidatedSourceDoesNotResurrect(t *testing.T) {
	source := &memSource{name: "demo", sessions: []AgentSession{{SessionKey: "stale"}}}
	s := NewService(nil, nil, nil, source)
	source.during = func() { s.Updated("demo", nil) }
	page, err := s.ListPage(context.Background(), sessionlist.Options{})
	if err != nil || page.Total != 0 {
		t.Fatalf("stale list published: %+v %v", page, err)
	}
	source.during = nil
	source.sessions = []AgentSession{{SessionKey: "fresh"}}
	page, err = s.ListPage(context.Background(), sessionlist.Options{})
	if err != nil || page.Total != 1 {
		t.Fatalf("fresh list missing: %+v %v", page, err)
	}
}

func TestPagedRemovedSourceDoesNotResurrect(t *testing.T) {
	a := &memSource{name: "first", sessions: []AgentSession{{SessionKey: "a"}}}
	b := &memSource{name: "second", sessions: []AgentSession{{SessionKey: "b"}}}
	s := NewService(nil, nil, nil, a, b)
	a.during = func() { s.Remove("second") }
	page, err := s.ListPage(context.Background(), sessionlist.Options{})
	if err != nil || page.Total != 1 {
		t.Fatalf("removed source listed: %+v %v", page, err)
	}
	a.during = nil
	counts, err := s.ListCounts(context.Background(), sessionlist.Options{})
	if err != nil || counts.Total != 1 {
		t.Fatalf("removed source counted: %+v %v", counts, err)
	}
}
