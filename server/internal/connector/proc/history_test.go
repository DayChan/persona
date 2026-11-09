package proc

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/serve"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/kbwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// historyProgram is exampleProgram that can also import history (made-up data): a dm of two pages, a group the account
// left, a chat whose source is pacing, and one whose read fails with a code.
type historyProgram struct{ *exampleProgram }

func (p *historyProgram) Manifest() connector.Manifest {
	m := p.exampleProgram.Manifest()
	m.Capabilities = append(m.Capabilities, connector.CapHistory, connector.CapHistoryAnchors)
	return m
}

func (*historyProgram) HistoryChats(context.Context) ([]connectorwire.HistoryChat, error) {
	return []connectorwire.HistoryChat{
		{ID: "dm1", Name: "Example A", Kind: connectorwire.HistoryKindDM, Estimate: 3},
		{ID: "gone", Name: "Example group", Kind: connectorwire.HistoryKindGroup, Muted: true, Source: "examplefeed"},
	}, nil
}

func (*historyProgram) History(_ context.Context, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	msg := func(pos int) kbwire.SourceMessage {
		return kbwire.SourceMessage{ID: "m" + strconv.Itoa(pos), Position: strconv.Itoa(pos), SenderName: "Example A",
			Text: "limit " + strconv.Itoa(q.Limit), At: at.Add(time.Duration(pos) * time.Minute), Mentioned: pos == 1}
	}
	switch {
	case q.Chat == "slow": // notes each read it gets in PERSONA_PROC_COUNT, then answers too late
		if f, err := os.OpenFile(os.Getenv("PERSONA_PROC_COUNT"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			fmt.Fprintln(f, "archive.history")
			f.Close()
		}
		time.Sleep(3 * time.Second) // sleep-ok: the fake program stands for a source that answers after the caller's timeout
		return connectorwire.HistoryPage{}, nil
	case q.Chat == "gone":
		return connectorwire.HistoryPage{Unreadable: true}, nil
	case q.Chat == "busy":
		return connectorwire.HistoryPage{Pace: &connectorwire.Pace{RetryAfterMS: 30000, Reason: "rate limited"}}, nil
	case q.Chat == "expired":
		return connectorwire.HistoryPage{}, &rpc.Error{Code: serve.CodeNotSignedIn, Message: "sign in again"}
	case q.Before == "2":
		return connectorwire.HistoryPage{Messages: []kbwire.SourceMessage{msg(0), msg(1)}, Newer: "1"}, nil
	}
	// More than the limit, as a source may answer.
	return connectorwire.HistoryPage{Messages: []kbwire.SourceMessage{msg(2), msg(3), msg(4)}, Older: "2", More: true}, nil
}

func (*historyProgram) HistoryAnchors(_ context.Context, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	hit := kbwire.SourceMessage{ID: "m9", Position: "9", SenderName: "Example B", Text: "a mention", At: q.Since, Mentioned: true}
	return connectorwire.HistoryAnchors{Anchors: []connectorwire.HistoryAnchor{
		{Chat: "gone", Source: "examplefeed", Position: "9", At: q.Since, Mention: true, Message: &hit},
		{Chat: "dm1", Position: "3", At: q.Since.Add(time.Hour), Mine: true},
	}, Next: q.Chat + "|" + q.Cursor + "|" + strconv.Itoa(q.Limit), More: true}, nil
}

// The history calls reach the program and come back field for field, under both openings: pages, unreadable,
// pace, anchors with their hit, a coded error as the program said it, and the hub's limit cut.
func TestProgramHistory(t *testing.T) {
	for _, c := range []struct {
		name string
		conn func(t *testing.T) *Conn
	}{
		{"connector protocol", func(t *testing.T) *Conn { return New(program(t, &memSecrets{}, "PERSONA_PROC_KIND=history")) }},
		{"plugin protocol", func(t *testing.T) *Conn {
			return linkedAs(t, &memSecrets{}, (&historyProgram{exampleProgram: &exampleProgram{}}).Manifest(), "PERSONA_PROC_KIND=history")
		}},
	} {
		t.Run(c.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			conn := c.conn(t)
			go conn.Run(ctx)
			waitUntil(t, "the program's status", func() bool { return conn.Status(ctx).State == connector.StateNotSignedIn })
			if got := connector.Capabilities(conn); !slices.Contains(got, "history") || !slices.Contains(got, "history_anchors") {
				t.Fatalf("the registry lists %v", got)
			}
			h, ok := connector.As[connector.History](conn)
			if !ok {
				t.Fatal("declared history, yet no History")
			}
			chats, err := h.HistoryChats(ctx)
			if err != nil || len(chats) != 2 || chats[1] != (connectorwire.HistoryChat{ID: "gone", Name: "Example group",
				Kind: connectorwire.HistoryKindGroup, Muted: true, Source: "examplefeed"}) {
				t.Fatalf("history chats %+v %v", chats, err)
			}
			page, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "dm1", Limit: 2})
			if err != nil || len(page.Messages) != 3 || !page.More || page.Older != "2" || page.Messages[0].Text != "limit 2" {
				t.Fatalf("newest page %+v %v", page, err)
			}
			older, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "dm1", Before: page.Older, Limit: 2})
			if err != nil || older.More || older.Older != "" || len(older.Messages) != 2 || !older.Messages[1].Mentioned ||
				!older.Messages[0].At.Equal(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)) {
				t.Fatalf("the start %+v %v", older, err)
			}
			if p, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "gone"}); err != nil || !p.Unreadable || len(p.Messages) != 0 {
				t.Fatalf("unreadable %+v %v", p, err)
			}
			if p, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "busy"}); err != nil || p.Pace == nil ||
				*p.Pace != (connectorwire.Pace{RetryAfterMS: 30000, Reason: "rate limited"}) {
				t.Fatalf("paced %+v %v", p, err)
			}
			var re *rpc.Error
			if _, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "expired"}); !errors.As(err, &re) || re.Code != serve.CodeNotSignedIn {
				t.Fatalf("a coded error: %v", err)
			}
			// The program's side holds the hub's bounds whoever calls it: a limit over HistoryMost is cut, two positions refused.
			if p, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "dm1", Limit: 5000}); err != nil || p.Messages[0].Text != "limit "+strconv.Itoa(connectorwire.HistoryMost) {
				t.Fatalf("a limit over the most %+v %v", p, err)
			}
			if _, err := h.History(ctx, connectorwire.HistoryQuery{Chat: "dm1", Before: "2", After: "1"}); err == nil {
				t.Fatal("before and after together went through")
			}
			a, ok := connector.As[connector.HistoryAnchors](conn)
			if !ok {
				t.Fatal("declared history_anchors, yet no HistoryAnchors")
			}
			since := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
			res, err := a.HistoryAnchors(ctx, connectorwire.HistoryAnchorsQuery{Since: since, Cursor: "c1", Limit: 10})
			if err != nil || len(res.Anchors) != 2 || res.Next != "|c1|10" || !res.More || res.Anchors[0].Message == nil ||
				res.Anchors[0].Message.ID != "m9" || res.Anchors[0].Source != "examplefeed" || !res.Anchors[0].At.Equal(since) || !res.Anchors[1].Mine {
				t.Fatalf("anchors %+v %v", res, err)
			}
			raw, _ := json.Marshal(res.Anchors[0])
			if string(raw) != `{"chat":"gone","source":"examplefeed","position":"9","at":"2026-01-01T00:00:00Z","mention":true,"message":{"id":"m9","position":"9","sender_name":"Example B","mentioned":true,"text":"a mention","at":"2026-01-01T00:00:00Z"}}` {
				t.Fatalf("an anchor on the wire: %s", raw)
			}
		})
	}
}

// A read that times out is "result unknown": the caller gets the timeout and the program got the read once, never
// again on the caller's own (a source that paces would count a retry against the account).
func TestProgramHistoryTimeoutNotRetried(t *testing.T) {
	shortCalls(t)
	count := filepath.Join(t.TempDir(), "count")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := New(program(t, &memSecrets{}, "PERSONA_PROC_KIND=history", "PERSONA_PROC_COUNT="+count))
	go conn.Run(ctx)
	waitUntil(t, "the program's status", func() bool { return conn.Status(ctx).State == connector.StateNotSignedIn })
	if _, err := conn.History(ctx, connectorwire.HistoryQuery{Chat: "slow"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("a read past the call timeout: %v, want the timeout", err)
	}
	// Long enough for a retry to have reached the program, short of the program's own answer.
	time.Sleep(time.Second) // sleep-ok: proving that no retry arrives needs a quiet period; nothing can be polled for
	raw, err := os.ReadFile(count)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "archive.history"); n != 1 {
		t.Fatalf("the program got the read %d times, want once", n)
	}
}

// A program that does not declare history refuses it with no_capability, and the hub never finds it through As.
func TestProgramWithoutHistory(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	conn := New(program(t, &memSecrets{}))
	go conn.Run(ctx)
	waitUntil(t, "the program's status", func() bool { return conn.Status(ctx).State == connector.StateNotSignedIn })
	if _, ok := connector.As[connector.History](conn); ok {
		t.Fatal("no history declared, yet the hub would find it")
	}
	var re *rpc.Error
	if _, err := conn.History(ctx, connectorwire.HistoryQuery{Chat: "dm1"}); !errors.As(err, &re) || re.Code != serve.CodeNoCapability {
		t.Fatalf("history of a program without it: %v", err)
	}
	if _, err := conn.HistoryChats(ctx); !errors.As(err, &re) || re.Code != serve.CodeNoCapability {
		t.Fatalf("history chats of a program without it: %v", err)
	}
}
