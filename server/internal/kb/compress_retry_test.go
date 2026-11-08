package kb

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/ai"
)

// failingModel answers every call with err, or with the fake digest once err is nil.
type failingModel struct {
	err   error
	calls int
}

func (m *failingModel) JSONMax(ctx context.Context, system, user string, n int, out any) error {
	m.calls++
	if m.err != nil {
		return m.err
	}
	return (&fakeModel{}).JSONMax(ctx, system, user, n, out)
}

func dueChat(t *testing.T, e *Engine) *Chat {
	t.Helper()
	at := time.Now().Add(-time.Hour)
	c := &Chat{ID: "lark-chat", Source: "lark", SourceID: "chat", Kind: "dm", Name: "张三", Created: at.Add(-time.Hour), LastAt: at}
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	if err := e.Store.AppendMessages(c, []Line{{At: at, Sender: "张三", Text: "示例：周五见"}}); err != nil {
		t.Fatal(err)
	}
	c.Pending = 1
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	return c
}

func isDue(e *Engine, id string, now time.Time) bool {
	for _, c := range e.DueForCompression(now) {
		if c.ID == id {
			return true
		}
	}
	return false
}

// The model declines the same messages every time: the batch is settled without a summary instead of being sent again
// every round (it was, every two minutes for three days).
func TestCompressSettlesARefusedBatch(t *testing.T) {
	m := &failingModel{err: ai.ErrRefused}
	e := testEngine(t, m)
	c := dueChat(t, e)
	err := e.Compress(context.Background(), c)
	if !errors.Is(err, ai.ErrRefused) {
		t.Fatalf("err = %v, want ErrRefused", err)
	}
	got, _ := e.Store.Chat(c.ID)
	if got.Pending != 0 || !got.CoveredUntil.Equal(c.LastAt.Add(time.Minute)) || got.CompressFailures != 0 {
		t.Errorf("chat = pending %d, covered until %v, failures %d; want 0, %v, 0", got.Pending, got.CoveredUntil, got.CompressFailures, c.LastAt.Add(time.Minute))
	}
	if !got.CompressedAt.IsZero() || e.Store.Summary(KindChat, c.ID) != "" {
		t.Errorf("a refused batch wrote a summary (compressed at %v)", got.CompressedAt)
	}
	if isDue(e, c.ID, time.Now()) {
		t.Error("the refused chat is still due")
	}
}

// Any other failure puts the chat off, longer with each failure in a row; a success clears that.
func TestCompressBacksOffAfterFailures(t *testing.T) {
	m := &failingModel{err: errors.New("ai: no JSON object in reply")}
	e := testEngine(t, m)
	c := dueChat(t, e)
	for i, want := range []time.Duration{compressRetry, 2 * compressRetry, 4 * compressRetry} {
		start := time.Now()
		if err := e.Compress(context.Background(), c); err == nil {
			t.Fatalf("failure %d: no error", i+1)
		}
		got, _ := e.Store.Chat(c.ID)
		if got.CompressFailures != i+1 || got.Pending != 1 {
			t.Fatalf("failure %d: failures %d, pending %d", i+1, got.CompressFailures, got.Pending)
		}
		if wait := got.CompressRetryAt.Sub(start); wait < want || wait > want+time.Minute {
			t.Errorf("failure %d: next try in %v, want %v", i+1, wait, want)
		}
		if isDue(e, c.ID, start) || !isDue(e, c.ID, got.CompressRetryAt) {
			t.Errorf("failure %d: due now or not due at %v", i+1, got.CompressRetryAt)
		}
	}

	m.err = nil
	if err := e.Compress(context.Background(), c); err != nil {
		t.Fatal(err)
	}
	got, _ := e.Store.Chat(c.ID)
	if got.CompressFailures != 0 || !got.CompressRetryAt.IsZero() || got.Pending != 0 {
		t.Errorf("after success: failures %d, retry at %v, pending %d", got.CompressFailures, got.CompressRetryAt, got.Pending)
	}
}

func TestCompressRetryIsCapped(t *testing.T) {
	m := &failingModel{err: errors.New("boom")}
	e := testEngine(t, m)
	c := dueChat(t, e)
	c.CompressFailures = 40 // a shift this far would overflow
	if err := e.Store.PutChat(c); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	_ = e.Compress(context.Background(), c)
	got, _ := e.Store.Chat(c.ID)
	if wait := got.CompressRetryAt.Sub(start); wait < compressRetryMax || wait > compressRetryMax+time.Minute {
		t.Errorf("next try in %v, want %v", wait, compressRetryMax)
	}
}

type compressCallback func(context.Context) error

func (m compressCallback) JSONMax(ctx context.Context, _, _ string, _ int, _ any) error {
	return m(ctx)
}

func TestRefusedCompressionKeepsConcurrentMessagesEligible(t *testing.T) {
	for _, offset := range []time.Duration{0, 30 * time.Second, 2 * time.Minute} {
		for _, recoverPending := range []bool{false, true} {
			t.Run(offset.String()+map[bool]string{false: "/committed", true: "/recovery"}[recoverPending], func(t *testing.T) {
				s, e := replayStore(t)
				at := time.Now().Add(-time.Hour).Truncate(time.Minute)
				ingestMessages(t, e, "example", "thread", 1, SourceMessage{ID: "first", At: at, Text: "First synthetic message"})
				c, _ := s.Chat("example-thread")
				c.CompressFailures, c.CompressRetryAt = 3, time.Now().Add(time.Hour)
				if err := s.PutChat(c); err != nil {
					t.Fatal(err)
				}
				second := SourceMessage{ID: "second", At: at.Add(offset), Text: "Second synthetic message"}
				e.Model = compressCallback(func(ctx context.Context) error {
					failure := errors.New("synthetic metadata failure")
					if recoverPending {
						s.ingestWrite = func(path string, raw []byte) error {
							if filepath.Base(path) == "meta.json" {
								return failure
							}
							return archiveWrite(path, raw)
						}
					}
					_, err := e.Ingest(ctx, named("example"), SourceChat{ID: "thread"}, []SourceMessage{second})
					s.ingestWrite = nil
					if recoverPending && !errors.Is(err, failure) || !recoverPending && err != nil {
						t.Fatalf("concurrent ingestion: %v", err)
					}
					return ai.ErrRefused
				})
				if err := e.Compress(context.Background(), c); !errors.Is(err, ai.ErrRefused) {
					t.Fatalf("refusal: %v", err)
				}
				assertArchiveCounts(t, s, c.ID, 2, 2)
				cur, _ := s.Chat(c.ID)
				if !cur.CoveredUntil.IsZero() || cur.CompressFailures != 0 || !cur.CompressRetryAt.IsZero() || !cur.CompressedAt.IsZero() || !isDue(e, c.ID, time.Now()) {
					t.Fatalf("refusal settled or postponed unseen messages: %+v", cur)
				}
				s, err := NewStore(s.Dir)
				if err != nil {
					t.Fatal(err)
				}
				model := &fakeModel{}
				e = &Engine{Store: s, Model: model}
				if !isDue(e, c.ID, time.Now()) {
					t.Fatal("unseen messages are not eligible after restart")
				}
				if err := e.Compress(context.Background(), cur); err != nil {
					t.Fatal(err)
				}
				assertArchiveCounts(t, s, c.ID, 2, 0)
				if len(model.calls) == 0 || !strings.Contains(model.calls[0], "First synthetic message") || !strings.Contains(model.calls[0], "Second synthetic message") {
					t.Fatalf("healthy retry missed messages: %v", model.calls)
				}
				ingestMessages(t, e, "example", "thread", 0, second)
				assertArchiveCounts(t, s, c.ID, 2, 0)
			})
		}
	}
}

func TestIngestReopensAnAlreadySettledMinute(t *testing.T) {
	for _, refused := range []bool{false, true} {
		t.Run(map[bool]string{false: "summary", true: "refusal"}[refused], func(t *testing.T) {
			s, e := replayStore(t)
			at := time.Now().Add(-time.Hour).Truncate(time.Minute)
			ingestMessages(t, e, "example", "thread", 1, SourceMessage{ID: "first", At: at, Text: "Same synthetic text"})
			e.Model = &failingModel{}
			if refused {
				e.Model = &failingModel{err: ai.ErrRefused}
			}
			c, _ := s.Chat("example-thread")
			if err := e.Compress(context.Background(), c); refused && !errors.Is(err, ai.ErrRefused) || !refused && err != nil {
				t.Fatalf("initial result: %v", err)
			}
			cur, _ := s.Chat(c.ID)
			if cur.Pending != 0 || !cur.CoveredUntil.Equal(at.Add(time.Minute)) || !cur.CompressRetryAt.IsZero() || isDue(e, c.ID, time.Now()) {
				t.Fatalf("unchanged snapshot was not settled: %+v", cur)
			}
			ingestMessages(t, e, "example", "thread", 1, SourceMessage{ID: "second", At: at, Text: "Same synthetic text"})
			cur, _ = s.Chat(c.ID)
			if cur.Pending != 1 || cur.CoveredUntil.After(at) || !isDue(e, c.ID, time.Now()) {
				t.Fatalf("later ID in a settled minute is not eligible: %+v", cur)
			}
			model := &fakeModel{}
			e.Model = model
			if err := e.Compress(context.Background(), cur); err != nil {
				t.Fatal(err)
			}
			if len(model.calls) == 0 || strings.Count(model.calls[0], "Same synthetic text") != 2 {
				t.Fatalf("healthy retry did not read the reopened minute: %v", model.calls)
			}
			assertArchiveCounts(t, s, c.ID, 2, 0)
		})
	}
}

func TestConcurrentCompressionErrorsKeepExistingRetryPolicy(t *testing.T) {
	for _, tc := range []struct {
		name    string
		err     error
		cancel  bool
		backoff bool
	}{
		{name: "failure", err: errors.New("synthetic model failure"), backoff: true},
		{name: "paused", err: &ai.PausedError{Pause: ai.Pause{Reason: ai.ReasonQuota, Until: time.Now().Add(time.Hour)}}},
		{name: "cancelled", err: context.Canceled, cancel: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, e := replayStore(t)
			at := time.Now().Add(-time.Hour)
			ingestMessages(t, e, "example", "thread", 1, SourceMessage{ID: "first", At: at, Text: "First synthetic message"})
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e.Model = compressCallback(func(ctx context.Context) error {
				if _, err := e.Ingest(ctx, named("example"), SourceChat{ID: "thread"}, []SourceMessage{{ID: "second", At: at, Text: "Second synthetic message"}}); err != nil {
					return err
				}
				if tc.cancel {
					cancel()
				}
				return tc.err
			})
			c, _ := s.Chat("example-thread")
			if err := e.Compress(ctx, c); !errors.Is(err, tc.err) {
				t.Fatalf("model error changed: %v", err)
			}
			assertArchiveCounts(t, s, c.ID, 2, 2)
			cur, _ := s.Chat(c.ID)
			if !cur.CoveredUntil.IsZero() || cur.CompressRetryAt.IsZero() == tc.backoff || isDue(e, c.ID, time.Now()) == tc.backoff {
				t.Fatalf("retry or coverage policy changed: %+v", cur)
			}
			if tc.backoff && (cur.CompressFailures != 1 || !isDue(e, c.ID, cur.CompressRetryAt)) || !tc.backoff && cur.CompressFailures != 0 {
				t.Fatalf("failure policy changed: %+v", cur)
			}
		})
	}
}
