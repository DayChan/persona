package kb

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

func replayStore(t *testing.T) (*Store, *Engine) {
	t.Helper()
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return s, &Engine{Store: s}
}

func ingestMessages(t *testing.T, e *Engine, source, chat string, want int, msgs ...SourceMessage) {
	t.Helper()
	n, err := e.Ingest(context.Background(), named(source), SourceChat{ID: chat, Name: "Example", Kind: "group"}, msgs)
	if err != nil || n != want {
		t.Fatalf("ingest %s/%s: added %d, want %d, error %v", source, chat, n, want, err)
	}
}

func assertArchiveCounts(t *testing.T, s *Store, id string, messages, pending int) string {
	t.Helper()
	c, ok := s.Chat(id)
	if !ok || c.Messages != messages || c.Pending != pending {
		t.Fatalf("archive %s: %+v, want messages %d, pending %d", id, c, messages, pending)
	}
	text := s.Since(id, time.Time{}, 1<<20)
	if n := strings.Count(text, "\n- "); n != messages {
		t.Fatalf("archive %s: %d visible lines, want %d: %q", id, n, messages, text)
	}
	return text
}

func TestIngestIdentityScopeAndReordering(t *testing.T) {
	s, e := replayStore(t)
	at := time.Now().Add(-time.Hour)
	a := SourceMessage{ID: "body", Position: "one", At: at, SenderName: "Example", Text: "Identical synthetic text"}
	b := a
	b.ID, b.Position = "comment", "two"
	ingestMessages(t, e, "example", "thread", 2, a, a, b)
	ingestMessages(t, e, "example", "thread", 0, b, a, b)
	// Position is pagination, not identity; a source can edit text or move a message.
	a.Position, a.Text, a.At = "changed", "Edited text", at.Add(time.Minute)
	ingestMessages(t, e, "example", "thread", 0, a)
	ingestMessages(t, e, "example", "another-thread", 1, a)
	ingestMessages(t, e, "another-source", "thread", 1, a)
	text := assertArchiveCounts(t, s, "example-thread", 2, 2)
	if strings.Count(text, "Identical synthetic text") != 2 || strings.Contains(text, "Edited text") {
		t.Fatalf("distinct IDs or first-write-wins lost: %q", text)
	}
	for _, id := range []string{"example-another-thread", "another-source-thread"} {
		assertArchiveCounts(t, s, id, 1, 1)
	}
	// Sanitized directory collisions must refuse to mix exact source/chat identities.
	ingestMessages(t, e, "example", "unsafe/id", 1, a)
	if _, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "unsafe?id"}, []SourceMessage{a}); err == nil {
		t.Fatal("mixed identities sharing a sanitized directory")
	}
}

func TestIngestLegacyAndMissingIDs(t *testing.T) {
	s, e := replayStore(t)
	at := time.Now().Add(-time.Hour)
	c := &Chat{ID: "example-thread", Source: "example", SourceID: "thread", Name: "Example", Messages: 1, Pending: 0}
	if err := s.AppendMessages(c, []Line{{At: at, Sender: "Example", Text: "Legacy synthetic text"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChat(c); err != nil {
		t.Fatal(err)
	}
	m := SourceMessage{ID: "newly-known", At: at, SenderName: "Example", Text: "Legacy synthetic text"}
	// No ID can be reconstructed from old text. First sight of an ID is new,
	// even if its content happens to match an old line, then it is replay-safe.
	ingestMessages(t, e, "example", "thread", 1, m)
	ingestMessages(t, e, "example", "thread", 0, m)
	m.ID, m.Position = "", "same-position"
	ingestMessages(t, e, "example", "thread", 2, m, m)
	ingestMessages(t, e, "example", "thread", 1, m)
	assertArchiveCounts(t, s, c.ID, 5, 4)
	if r, err := s.receipts("example", "thread", c.ID); err != nil || len(r.IDs) != 1 {
		t.Fatalf("invented identities: %+v, %v", r, err)
	}
}

// Stop before/after each durable replacement: intent, two days, receipts, meta.
// Reopen uses only disk state, and retry must neither lose lines nor count twice.
func TestIngestRecoversEveryWriteBoundary(t *testing.T) {
	for write := 1; write <= 5; write++ {
		for _, after := range []bool{false, true} {
			for _, reopen := range []bool{false, true} {
				t.Run(fmt.Sprintf("write-%d/after-%v/reopen-%v", write, after, reopen), func(t *testing.T) {
					s, e := replayStore(t)
					at := time.Now().Add(-time.Hour)
					base := SourceMessage{ID: "base", At: at.AddDate(0, 0, -2), SenderName: "Example", Text: "Original"}
					msgs := []SourceMessage{
						{ID: "first", At: at.AddDate(0, 0, -1), SenderName: "Example", Text: "First new message"},
						{ID: "second", At: at, SenderName: "Example", Text: "Second new message"},
					}
					ingestMessages(t, e, "example", "thread", 1, base)
					c, _ := s.Chat("example-thread")
					c.Pending = 0 // the original was already compressed
					c.Pinned, c.LockedFields = "Keep this fact", []string{"name"}
					if err := s.PutChat(c); err != nil {
						t.Fatal(err)
					}
					injected := errors.New("synthetic archive write failure")
					calls, stopped := 0, false
					s.ingestWrite = func(path string, raw []byte) error {
						if stopped {
							return injected
						}
						calls++
						if calls == write && !after {
							stopped = true
							return injected
						}
						if err := archiveWrite(path, raw); err != nil {
							return err
						}
						if calls == write {
							stopped = true
							return injected
						}
						return nil
					}
					if n, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, msgs); n != 0 || !errors.Is(err, injected) || !stopped {
						t.Fatalf("failure not exercised: %d, %v, stopped %v", n, err, stopped)
					}
					pending := write > 1 || after
					if pending {
						// Unresolved snapshots cannot be overwritten by another writer.
						if err := s.PutChat(c); err == nil {
							t.Fatal("metadata accepted during pending recovery")
						}
						if err := s.AppendMessages(c, []Line{{At: at, Text: "uncommitted"}}); err == nil {
							t.Fatal("append accepted during pending recovery")
						}
						if _, err := s.MergeMessages(c, []Line{{At: at, Text: "uncommitted"}}); err == nil {
							t.Fatal("history merge accepted during pending recovery")
						}
						if _, err := OpenReadOnly(s.Dir); err == nil {
							t.Fatal("read-only open accepted unfinished ingestion")
						}
					}
					if reopen {
						var err error
						s, err = NewStore(s.Dir)
						if err != nil {
							t.Fatal(err)
						}
						e = &Engine{Store: s}
					} else {
						s.ingestWrite = nil
					}
					want := 2
					if pending {
						want = 0 // the durable intent finishes the interrupted batch
					}
					ingestMessages(t, e, "example", "thread", want, msgs...)
					ingestMessages(t, e, "example", "thread", 0, msgs[1], base, msgs[0])
					text := assertArchiveCounts(t, s, c.ID, 3, 2)
					for _, m := range append(msgs, base) {
						if strings.Count(text, m.Text) != 1 {
							t.Fatalf("missing or repeated %q: %q", m.ID, text)
						}
					}
					cur, _ := s.Chat(c.ID)
					if cur.Pinned != c.Pinned || cur.Name != "Example" {
						t.Fatalf("lost protected fields: %+v", cur)
					}
					if _, err := os.Stat(s.path(KindChat, c.ID, ingestJournal)); !os.IsNotExist(err) {
						t.Fatalf("unfinished journal: %v", err)
					}
				})
			}
		}
	}
}

type replayDownloadSource struct {
	named
	downloads int
}

func (s *replayDownloadSource) Download(_ context.Context, _ string, dst string) error {
	s.downloads++
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte("synthetic attachment"), 0o600)
}

func TestIngestReplayKeepsAttachmentsAndSurvivesRezone(t *testing.T) {
	s, e := replayStore(t)
	if _, err := s.Rezone(time.UTC); err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	src := &replayDownloadSource{named: "example"}
	m := SourceMessage{ID: "image", At: at, SenderName: "Example", Text: "Synthetic picture", Type: "IMAGE", Media: []string{"image.png"}}
	chat := SourceChat{ID: "thread", Name: "Example"}
	if n, err := e.Ingest(context.Background(), src, chat, []SourceMessage{m}); n != 1 || err != nil {
		t.Fatalf("image ingest: %d, %v", n, err)
	}
	path := s.path(KindChat, "example-thread", "attachments", at.UTC().Format("2006-01-02"), "messages", attachmentName("image", 0, "image.png", "IMAGE"))
	if raw, err := os.ReadFile(path); err != nil || string(raw) != "synthetic attachment" {
		t.Fatalf("attachment lost: %q, %v", raw, err)
	}
	if _, err := s.Rezone(zone(t, "Asia/Tokyo")); err != nil {
		t.Fatal(err)
	}
	if n, err := e.Ingest(context.Background(), src, chat, []SourceMessage{m}); n != 0 || err != nil {
		t.Fatalf("image replay: %d, %v", n, err)
	}
	if src.downloads != 1 {
		t.Fatalf("replayed attachment downloaded %d times", src.downloads)
	}
	text := assertArchiveCounts(t, s, "example-thread", 1, 1)
	rel, _ := filepath.Rel(s.path(KindChat, "example-thread"), path)
	if !strings.Contains(text, rel) {
		t.Fatalf("attachment link changed after rezone: %q", text)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
}

func TestIngestFailedReceiptPruneDoesNotSuppressReusedID(t *testing.T) {
	s, e := replayStore(t)
	now := time.Now()
	m := SourceMessage{ID: "reusable", At: now.AddDate(0, 0, -3), Text: "Old synthetic text"}
	ingestMessages(t, e, "example", "thread", 1, m)
	s.ingestWrite = func(string, []byte) error { return errors.New("synthetic receipt cleanup failure") }
	if n := s.Prune(now, 1, 30); n != 1 {
		t.Fatalf("pruned %d days, want 1", n)
	}
	if r, err := s.receipts("example", "thread", "example-thread"); err != nil || len(r.IDs) != 1 {
		t.Fatalf("cleanup failure not exercised: %+v, %v", r, err)
	}
	s.ingestWrite = nil
	e.TextDays = 1
	m.At, m.Text = now.Add(-time.Hour), "New synthetic text"
	ingestMessages(t, e, "example", "thread", 1, m)
	if c, _ := s.Chat("example-thread"); c.Messages != 2 || c.Pending != 2 {
		t.Fatalf("cumulative counters after retention: %+v", c)
	}
}

func TestIngestFilesystemRecoveryWaitsForRepair(t *testing.T) {
	s, e := replayStore(t)
	at := time.Now().Add(-time.Hour)
	m := SourceMessage{ID: "one", At: at, Text: "Synthetic recovery message"}
	receipt := s.path(KindChat, "example-thread", ingestReceipts)
	// Install the obstruction after the intent and day have really been replaced.
	s.ingestWrite = func(path string, raw []byte) error {
		if path == receipt {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return err
			}
		}
		return archiveWrite(path, raw)
	}
	if _, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, []SourceMessage{m}); err == nil {
		t.Fatal("receipt replacement did not fail")
	}
	if _, err := NewStore(s.Dir); err == nil {
		t.Fatal("opened an archive whose pending receipt cannot be recovered")
	}
	if err := os.Remove(receipt); err != nil {
		t.Fatal(err)
	}
	s, err := NewStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ingestMessages(t, &Engine{Store: s}, "example", "thread", 0, m)
	assertArchiveCounts(t, s, "example-thread", 1, 1)
}

func TestIngestOtherPipelineWritersRecoverBeforeCounters(t *testing.T) {
	for _, writer := range []string{"history", "compression", "prune", "forget"} {
		t.Run(writer, func(t *testing.T) {
			s, e := replayStore(t)
			at := time.Now().Add(-time.Hour)
			m := SourceMessage{ID: "live", At: at, Text: "Live synthetic text"}
			s.ingestWrite = func(path string, raw []byte) error {
				if filepath.Base(path) == "meta.json" {
					return errors.New("synthetic metadata write failure")
				}
				return archiveWrite(path, raw)
			}
			if _, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, []SourceMessage{m}); err == nil {
				t.Fatal("metadata replacement did not fail")
			}
			s.ingestWrite = nil
			messages, pending := 1, 1
			switch writer {
			case "history":
				if n, err := e.WriteHistory(context.Background(), &Chat{Source: "example", SourceID: "thread"},
					[]SourceMessage{{ID: "old", At: at.Add(-time.Hour), Text: "Historical synthetic text"}}, HistoryWrite{}); n != 1 || err != nil {
					t.Fatalf("history recovery: %d, %v", n, err)
				}
				messages = 2
			case "compression":
				if ok, err := e.putCompressed(&Chat{ID: "example-thread"}, func(c *Chat) { c.Pending = 0 }); !ok || err != nil {
					t.Fatalf("compression recovery: %v, %v", ok, err)
				}
				pending = 0
			case "prune":
				if n := e.Prune(time.Now()); n != 0 {
					t.Fatalf("pruned retained data: %d", n)
				}
			case "forget":
				if f, err := e.ForgetSource("example"); err != nil || len(f.Chats) != 1 {
					t.Fatalf("forget recovery: %+v, %v", f, err)
				}
				ingestMessages(t, e, "example", "thread", 1, m)
			}
			ingestMessages(t, e, "example", "thread", 0, m)
			assertArchiveCounts(t, s, "example-thread", messages, pending)
		})
	}
}

type ingestDuringCompression struct {
	e  *Engine
	at time.Time
}

func (m ingestDuringCompression) JSONMax(ctx context.Context, system, user string, limit int, out any) error {
	if _, err := m.e.Ingest(ctx, named("example"), SourceChat{ID: "thread"}, []SourceMessage{{
		ID: "second", At: m.at, SenderName: "Example", Text: "Same synthetic text",
	}}); err != nil {
		return err
	}
	return (&fakeModel{}).JSONMax(ctx, system, user, limit, out)
}

func TestIngestDuringCompressionStaysPending(t *testing.T) {
	for _, offset := range []time.Duration{0, 30 * time.Second, 2 * time.Minute} {
		t.Run(offset.String(), func(t *testing.T) {
			s, e := replayStore(t)
			at := time.Now().Add(-time.Hour).Truncate(time.Minute)
			ingestMessages(t, e, "example", "thread", 1, SourceMessage{
				ID: "first", At: at, SenderName: "Example", Text: "Same synthetic text",
			})
			e.Model = ingestDuringCompression{e: e, at: at.Add(offset)}
			c, _ := s.Chat("example-thread")
			if err := e.Compress(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			assertArchiveCounts(t, s, c.ID, 2, 2)
			if !c.CoveredUntil.IsZero() {
				t.Fatalf("coverage advanced past concurrent pending work: %v", c.CoveredUntil)
			}
			model := &fakeModel{}
			e.Model = model
			if err := e.Compress(context.Background(), c); err != nil {
				t.Fatal(err)
			}
			assertArchiveCounts(t, s, c.ID, 2, 0)
			if len(model.calls) == 0 || strings.Count(model.calls[0], "Same synthetic text") != 2 {
				t.Fatalf("next compression missed concurrent messages: %v", model.calls)
			}
		})
	}
}

func TestIngestUnreadableRecoveryFailsClosed(t *testing.T) {
	s, e := replayStore(t)
	m := SourceMessage{ID: "one", At: time.Now().Add(-time.Hour), Text: "Synthetic"}
	path := s.path(KindChat, "example-thread", ingestReceipts)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, []SourceMessage{m}); err == nil {
		t.Fatal("unreadable receipts treated as no receipts")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	journal := s.path(KindChat, "example-thread", ingestJournal)
	if err := os.WriteFile(journal, []byte(`{"version":999}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := NewStore(s.Dir); err == nil {
		t.Fatal("invalid recovery ignored on writable open")
	}
	if _, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, []SourceMessage{m}); err == nil {
		t.Fatal("invalid recovery ignored on retry")
	}
}

func TestIngestConcurrentReplayAndHistory(t *testing.T) {
	s, e := replayStore(t)
	at := time.Now().Add(-time.Hour)
	m := SourceMessage{ID: "live", At: at, SenderName: "Example", Text: "Live synthetic message"}
	var wg sync.WaitGroup
	errs := make(chan error, 16)
	for i := 0; i < 8; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread", Name: "Example"}, []SourceMessage{m, m})
			errs <- err
		}()
		go func() {
			defer wg.Done()
			_, err := e.WriteHistory(context.Background(), &Chat{ID: "example-thread", Source: "example", SourceID: "thread", Name: "Example"},
				[]SourceMessage{{ID: "history", At: at.Add(-time.Hour), SenderName: "Example", Text: "Historical synthetic message"}}, HistoryWrite{Mode: HistoryFull})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertArchiveCounts(t, s, "example-thread", 2, 1)
	s, err := NewStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ingestMessages(t, &Engine{Store: s}, "example", "thread", 0, m)
	assertArchiveCounts(t, s, "example-thread", 2, 1)
}

type waitingReplaySource struct {
	named
	started chan struct{}
	release chan struct{}
}

func (s *waitingReplaySource) Download(_ context.Context, _ string, _ string) error {
	close(s.started)
	<-s.release
	return errors.New("synthetic unavailable attachment")
}

func TestIngestPreservesHistoryWrittenDuringDownload(t *testing.T) {
	s, e := replayStore(t)
	at := time.Now().Add(-time.Hour)
	src := &waitingReplaySource{named: "example", started: make(chan struct{}), release: make(chan struct{})}
	defer func() {
		select {
		case <-src.release:
		default:
			close(src.release)
		}
	}()
	live := SourceMessage{ID: "live", At: at, Text: "Live synthetic message", Type: "IMAGE", Media: []string{"image"}}
	liveDone := make(chan error, 1)
	go func() {
		_, err := e.Ingest(context.Background(), src, SourceChat{ID: "thread", Name: "Example"}, []SourceMessage{live})
		liveDone <- err
	}()
	<-src.started
	historyDone := make(chan error, 1)
	go func() {
		_, err := e.WriteHistory(context.Background(), &Chat{Source: "example", SourceID: "thread", Name: "Example"},
			[]SourceMessage{{ID: "old", At: at.Add(-time.Hour), Text: "Historical synthetic message"}}, HistoryWrite{Mode: HistoryFull})
		historyDone <- err
	}()
	select {
	case err := <-historyDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("history was blocked by a connector's attachment download")
	}
	close(src.release)
	if err := <-liveDone; err != nil {
		t.Fatal(err)
	}
	assertArchiveCounts(t, s, "example-thread", 2, 1)
	c, _ := s.Chat("example-thread")
	if c.HistoryMode != HistoryFull {
		t.Fatalf("history metadata overwritten after download: %+v", c)
	}
}

func TestIngestRetentionAndForgetReceipts(t *testing.T) {
	s, e := replayStore(t)
	now := time.Now()
	old := SourceMessage{ID: "reusable", At: now.AddDate(0, 0, -3), SenderName: "Example", Text: "Expired synthetic message"}
	ingestMessages(t, e, "example", "thread", 1, old)
	ingestMessages(t, e, "other", "thread", 1, old)
	if n := s.Prune(now, 1, 30); n != 2 {
		t.Fatalf("pruned %d days, want 2", n)
	}
	if r, err := s.receipts("example", "thread", "example-thread"); err != nil || len(r.IDs) != 0 {
		t.Fatalf("retained expired receipts: %+v, %v", r, err)
	}
	old.At, old.Text = now.Add(-time.Hour), "New synthetic message with reused ID"
	ingestMessages(t, e, "example", "thread", 1, old)
	if _, err := e.ForgetSource("example"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(s.Dir, KindChat, "example-thread")); !os.IsNotExist(err) {
		t.Fatalf("forgotten chat or receipts remain: %v", err)
	}
	s, err := NewStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	e = &Engine{Store: s}
	ingestMessages(t, e, "example", "thread", 1, old)
	assertArchiveCounts(t, s, "example-thread", 1, 1)
	if _, ok := s.Chat("other-thread"); !ok {
		t.Fatal("forgot another source")
	}
}
