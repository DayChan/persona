package assistant

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/kb"
)

type replayArchive struct {
	batches []connector.ChatBatch
	cursors []string
}

func (a *replayArchive) Changes(_ context.Context, cursor string, _ int) ([]connector.ChatBatch, string, bool, error) {
	a.cursors = append(a.cursors, cursor)
	if cursor == "next" {
		return nil, cursor, false, nil
	}
	return a.batches, "next", false, nil
}

func TestArchiveConnectorCheckpointRetry(t *testing.T) {
	for _, restart := range []bool{false, true} {
		t.Run(map[bool]string{false: "retry", true: "reopen"}[restart], func(t *testing.T) {
			st, err := kb.NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			checkpoint := filepath.Join(st.Dir, "cursors", "example.json")
			if err := os.MkdirAll(checkpoint, 0o700); err != nil {
				t.Fatal(err)
			}
			a := &replayArchive{batches: []connector.ChatBatch{{
				Chat: kb.SourceChat{ID: "thread", Name: "Example", Kind: "group"},
				Messages: []kb.SourceMessage{{ID: "body", Position: "position", SenderName: "Example",
					Text: "Synthetic replay message", At: time.Now().Add(-time.Hour)}},
			}}}
			if n, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, "example", a); n != 1 || err == nil {
				t.Fatalf("checkpoint failure: added %d, error %v", n, err)
			}
			if err := os.Remove(checkpoint); err != nil {
				t.Fatal(err)
			}
			if restart {
				st, err = kb.NewStore(st.Dir)
				if err != nil {
					t.Fatal(err)
				}
			}
			n, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, "example", a)
			c, ok := st.Chat(kb.ChatID("example", "thread"))
			if !ok {
				t.Fatal("missing archived chat")
			}
			text := st.Since(c.ID, time.Time{}, 10000)
			if err != nil || n != 0 || c.Messages != 1 || c.Pending != 1 || strings.Count(text, "Synthetic replay message") != 1 {
				t.Fatalf("replayed durable message: added %d, error %v, chat %+v, archive %q", n, err, c, text)
			}
			if len(a.cursors) != 2 || a.cursors[0] != "" || a.cursors[1] != "" {
				t.Fatalf("retry cursors: %v", a.cursors)
			}
		})
	}
}

func TestArchiveConnectorRetriesEarlierBatchAfterLaterFailure(t *testing.T) {
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-time.Hour)
	a := &replayArchive{}
	for _, id := range []string{"first", "second"} {
		a.batches = append(a.batches, connector.ChatBatch{
			Chat:     kb.SourceChat{ID: id, Name: "Example", Kind: "group"},
			Messages: []kb.SourceMessage{{ID: "body", At: at, SenderName: "Example", Text: "Synthetic " + id}},
		})
	}
	blocked := st.Path(kb.KindChat, "example-second", "messages", at.In(st.Zone()).Format("2006-01-02")+".md")
	if err := os.MkdirAll(blocked, 0o700); err != nil {
		t.Fatal(err)
	}
	if n, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, "example", a); n != 1 || err == nil {
		t.Fatalf("later batch failure: added %d, error %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(st.Dir, "cursors", "example.json")); !os.IsNotExist(err) {
		t.Fatalf("checkpoint advanced over a failed batch: %v", err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	st, err = kb.NewStore(st.Dir)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, "example", a); n != 1 || err != nil {
		t.Fatalf("retry page: added %d, error %v", n, err)
	}
	for _, id := range []string{"first", "second"} {
		c, ok := st.Chat("example-" + id)
		if !ok || c.Messages != 1 || c.Pending != 1 {
			t.Fatalf("chat %s: %+v", id, c)
		}
		if text := st.Since(c.ID, time.Time{}, 10000); strings.Count(text, "Synthetic "+id) != 1 {
			t.Fatalf("batch replay duplicated or lost: %q", text)
		}
	}
}

func TestArchiveConnectorFailedAtomicCheckpointPreservesPrevious(t *testing.T) {
	st, err := kb.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	// The existing filename fits the filesystem component limit, but a temporary
	// filename with the atomic writer's suffix cannot. This deterministically
	// fails replacement while leaving the previous readable checkpoint in place.
	id := strings.Repeat("x", 248)
	path := filepath.Join(st.Dir, "cursors", id+".json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	previous := []byte(`{"cursor":"previous"}`)
	if err := os.WriteFile(path, previous, 0o600); err != nil {
		t.Fatal(err)
	}
	a := &replayArchive{batches: []connector.ChatBatch{{
		Chat:     kb.SourceChat{ID: "thread", Name: "Example"},
		Messages: []kb.SourceMessage{{ID: "body", At: time.Now().Add(-time.Hour), Text: "Synthetic checkpoint control"}},
	}}}
	if n, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, id, a); n != 1 || err == nil {
		t.Fatalf("atomic replacement failure: added %d, error %v", n, err)
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != string(previous) {
		t.Fatalf("previous checkpoint lost: %q, %v", raw, err)
	}
	if n, err := archiveConnector(context.Background(), &kb.Engine{Store: st}, id, a); n != 0 || err == nil {
		t.Fatalf("retry with failed checkpoint: added %d, error %v", n, err)
	}
	if len(a.cursors) != 2 || a.cursors[0] != "previous" || a.cursors[1] != "previous" {
		t.Fatalf("previous cursor not reused: %v", a.cursors)
	}
}
