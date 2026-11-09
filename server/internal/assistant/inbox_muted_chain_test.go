package assistant

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/i18n"
)

// A mute files an item as read for compatibility, but must not claim that the user read it elsewhere.
func TestInboxMutedChain(t *testing.T) {
	for _, locale := range []struct {
		lang        i18n.Lang
		muted, read string
	}{
		{i18n.En, "Muted, done", "Read elsewhere, done"},
		{i18n.ZhHans, "已静音，已处理", "在别处看过了，已处理"},
		{i18n.Ja, "ミュート済み、対応済み", "ほかで既読、処理済み"},
	} {
		t.Run(string(locale.lang), func(t *testing.T) {
			for _, path := range []string{"applyMutes", "sortBatch", "putEvent", "read"} {
				t.Run(path, func(t *testing.T) {
					h := newInboxHub(t)
					b := &h.inbox
					b.lang = func() i18n.Lang { return locale.lang }
					now := time.Now().UTC()
					mute := InboxMute{Source: "example", Tag: connector.TagRepo, Value: "example/repository", Name: "Repository"}
					b.mutes = []InboxMute{mute}
					tags := []connector.Tag{{Key: mute.Tag, Value: mute.Value, Name: mute.Name}}
					it := &InboxItem{ID: "item", Source: mute.Source, Last: "1", Title: "Example", Context: "New activity",
						Tier: TierAction, UpdatedAt: now, Connector: &InboxConnector{Tags: tags}}
					want := locale.muted
					switch path {
					case "applyMutes":
						b.mu.Lock()
						b.items[it.ID] = it
						writes := b.applyMutes(now)
						b.mu.Unlock()
						if len(writes) != 1 {
							t.Fatalf("mute writes = %d, want 1", len(writes))
						}
					case "sortBatch":
						batch := newInboxBatch()
						batch.items[it.ID] = it
						b.mu.Lock()
						kept, todo := b.sortBatch(batch, nil, now)
						b.mu.Unlock()
						if len(kept) != 1 || len(todo) != 0 {
							t.Fatalf("muted admission: kept=%d todo=%d", len(kept), len(todo))
						}
					case "putEvent":
						it = h.putEvent(assistantwire.InboxSource{ID: mute.Source}, "thread",
							plannedEvent{messages: []string{"message"}, title: "Example", tier: TierAction},
							connector.InboxThing{ID: it.ID, Last: it.Last, At: now, Tags: tags}, true)
						if it == nil {
							t.Fatal("muted event was not kept")
						}
					case "read":
						want = locale.read
						b.mu.Lock()
						b.file(it, now, "read")
						b.mu.Unlock()
					}
					if it.open() || it.DoneBy != "read" {
						t.Fatalf("filing: done_at=%v done_by=%q", it.DoneAt, it.DoneBy)
					}
					if path != "read" && it.Reason != b.mutedReason(mute.Name) {
						t.Errorf("mute reason = %q, want %q", it.Reason, b.mutedReason(mute.Name))
					}
					done := 0
					for _, e := range it.Chain {
						if e.Kind == ChainDone {
							done++
							if e.Text != want || !e.At.Equal(*it.DoneAt) {
								t.Errorf("done entry = %+v, want text %q at %v", e, want, it.DoneAt)
							}
						}
					}
					if done != 1 {
						t.Errorf("done entries = %d, want 1", done)
					}
				})
			}
		})
	}
}
