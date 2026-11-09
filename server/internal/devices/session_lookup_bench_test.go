package devices

import (
	"fmt"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

// BenchmarkSessionLookupAll: one lookup per listed session (what the assistant's fleet does per change), 300 sessions.
func BenchmarkSessionLookupAll(b *testing.B) {
	m := &Machine{}
	for i := range 300 {
		m.sessions = append(m.sessions, proto.Session{SessionID: fmt.Sprintf("s%03d", i), Cwd: "/code/app", Title: "a session"})
	}
	b.ReportAllocs()
	for b.Loop() {
		for i := range m.sessions {
			if _, ok := m.session(m.sessions[i].SessionID); !ok {
				b.Fatal("missing")
			}
		}
	}
}

// sessionsWhere copies out the kept sessions in their order and nothing of the rest; the machine's list stays as it was.
func TestSessionsWhereCopiesKept(t *testing.T) {
	m := &Machine{}
	for i := range 300 {
		m.sessions = append(m.sessions, proto.Session{SessionID: fmt.Sprintf("s%03d", i), Title: "a session"})
	}
	got := m.sessionsWhere(func(s *proto.Session) bool { return s.SessionID == "s007" || s.SessionID == "s250" })
	if len(got) != 2 || got[0].SessionID != "s007" || got[1].SessionID != "s250" {
		t.Fatalf("kept %+v", got)
	}
	got[0].Title = "changed"
	if m.sessions[7].Title != "a session" {
		t.Fatal("the copy is the machine's list")
	}
	if n := testing.AllocsPerRun(20, func() { m.sessionsWhere(func(*proto.Session) bool { return false }) }); n != 0 {
		t.Fatalf("keeping none allocates %v times", n)
	}
}
