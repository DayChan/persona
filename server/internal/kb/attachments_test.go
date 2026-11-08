package kb

import (
	"context"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

type identityAttachmentSource struct {
	named
	asked []string
}

func (s *identityAttachmentSource) Download(_ context.Context, ref, dst string) error {
	s.asked = append(s.asked, ref)
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return err
	}
	return os.WriteFile(dst, []byte(ref), 0o600)
}

func attachmentLinks(s *Store, id string) []string {
	var out []string
	for _, match := range regexp.MustCompile(`\]\((attachments/[^)\r\n]+)\)`).FindAllStringSubmatch(s.Since(id, time.Time{}, 1<<20), -1) {
		out = append(out, filepath.Join(s.path(KindChat, id), match[1]))
	}
	return out
}

func assertAttachmentBytes(t *testing.T, path, want string) {
	t.Helper()
	if got, err := os.ReadFile(path); err != nil || string(got) != want {
		t.Fatalf("attachment %s: %q, want %q, error %v", filepath.Base(path), got, want, err)
	}
}

func TestDistinctMessageIDsKeepTheirOwnAttachments(t *testing.T) {
	ids := map[string][2]string{
		"normalized": {"asset/a", "asset?a"},
		"case":       {"ASSET", "asset"},
		"long":       {strings.Repeat("segment-", 80) + "a", strings.Repeat("segment-", 80) + "b"},
	}
	for name, pair := range ids {
		for _, batch := range []bool{false, true} {
			t.Run(name+map[bool]string{false: "/restart", true: "/batch"}[batch], func(t *testing.T) {
				s, e := replayStore(t)
				src := &identityAttachmentSource{named: "example"}
				at := time.Now().Add(-time.Hour)
				chat := SourceChat{ID: "thread", Name: "Example"}
				msgs := []SourceMessage{
					{ID: pair[0], At: at, SenderName: "Example", Text: "Identical synthetic picture", Type: "IMAGE", Media: []string{"first.png"}},
					{ID: pair[1], At: at, SenderName: "Example", Text: "Identical synthetic picture", Type: "IMAGE", Media: []string{"second.png"}},
				}
				if batch {
					if n, err := e.Ingest(context.Background(), src, chat, append(msgs, msgs[0])); n != 2 || err != nil {
						t.Fatalf("batch: %d, %v", n, err)
					}
				} else {
					if n, err := e.Ingest(context.Background(), src, chat, msgs[:1]); n != 1 || err != nil {
						t.Fatalf("first: %d, %v", n, err)
					}
					links := attachmentLinks(s, "example-thread")
					if len(links) != 1 {
						t.Fatalf("first links: %v", links)
					}
					assertAttachmentBytes(t, links[0], "first.png")
					var err error
					s, err = NewStore(s.Dir)
					if err != nil {
						t.Fatal(err)
					}
					e = &Engine{Store: s}
					if n, err := e.Ingest(context.Background(), src, chat, msgs[1:]); n != 1 || err != nil {
						t.Fatalf("second: %d, %v", n, err)
					}
					assertAttachmentBytes(t, links[0], "first.png")
				}
				links := attachmentLinks(s, "example-thread")
				if len(links) != 2 || strings.EqualFold(links[0], links[1]) {
					t.Fatalf("distinct raw IDs share an attachment path: %v", links)
				}
				for i, p := range links {
					if len(filepath.Base(p)) > 100 {
						t.Fatalf("unbounded attachment filename: %d bytes", len(filepath.Base(p)))
					}
					assertAttachmentBytes(t, p, msgs[i].Media[0])
				}
				// Replay and edited bodies of known IDs never redownload or mutate files.
				msgs[0].Media = []string{"edited.png"}
				s, err := NewStore(s.Dir)
				if err != nil {
					t.Fatal(err)
				}
				e = &Engine{Store: s}
				if n, err := e.Ingest(context.Background(), src, chat, []SourceMessage{msgs[1], msgs[0]}); n != 0 || err != nil {
					t.Fatalf("replay: %d, %v", n, err)
				}
				if len(src.asked) != 2 {
					t.Fatalf("replayed attachments were downloaded: %v", src.asked)
				}
				assertAttachmentBytes(t, links[0], "first.png")
				assertAttachmentBytes(t, links[1], "second.png")
				assertArchiveCounts(t, s, "example-thread", 2, 2)
			})
		}
	}
}

func TestAttachmentLegacyLinksRemainUntouched(t *testing.T) {
	for _, receipted := range []bool{false, true} {
		t.Run(map[bool]string{false: "unreceipted", true: "receipted"}[receipted], func(t *testing.T) {
			s, _ := replayStore(t)
			at := time.Now().Add(-time.Hour)
			c := &Chat{ID: "example-thread", Source: "example", SourceID: "thread", Name: "Example", Messages: 1}
			rel := filepath.Join("attachments", at.In(s.Zone()).Format("2006-01-02"), "asset_a-0.png")
			old := filepath.Join(s.path(KindChat, c.ID), rel)
			if err := archiveWrite(old, []byte("legacy attachment")); err != nil {
				t.Fatal(err)
			}
			if err := s.AppendMessages(c, []Line{{At: at, Sender: "Example", Text: "Legacy picture " + attachmentPlaceholder + "(" + rel + ")"}}); err != nil {
				t.Fatal(err)
			}
			if err := s.PutChat(c); err != nil {
				t.Fatal(err)
			}
			if receipted {
				if err := writeJSON(s.path(KindChat, c.ID, ingestReceipts), messageReceipts{Source: "example", Chat: "thread", IDs: map[string]time.Time{"asset/a": at}}); err != nil {
					t.Fatal(err)
				}
			}
			s, err := NewStore(s.Dir)
			if err != nil {
				t.Fatal(err)
			}
			src := &identityAttachmentSource{named: "example"}
			e := &Engine{Store: s}
			msg := SourceMessage{ID: "asset/a", At: at, Text: "Newly delivered picture", Type: "IMAGE", Media: []string{"new.png"}}
			want := 1
			if receipted {
				want = 0
			}
			if n, err := e.Ingest(context.Background(), src, SourceChat{ID: "thread"}, []SourceMessage{msg}); n != want || err != nil {
				t.Fatalf("legacy delivery: %d, %v", n, err)
			}
			if n, err := e.Ingest(context.Background(), src, SourceChat{ID: "thread"}, []SourceMessage{msg}); n != 0 || err != nil {
				t.Fatalf("legacy replay: %d, %v", n, err)
			}
			if len(src.asked) != want {
				t.Fatalf("unnecessary legacy redownload: %v", src.asked)
			}
			assertAttachmentBytes(t, old, "legacy attachment")
			links := attachmentLinks(s, c.ID)
			if len(links) != 1+want || links[0] != old {
				t.Fatalf("legacy links rewritten: %v", links)
			}
			assertArchiveCounts(t, s, c.ID, 1+want, want)
		})
	}
}

func TestAttachmentHashShapedLegacyFilenameRemainsUntouched(t *testing.T) {
	s, e := replayStore(t)
	at := time.Now().Add(-time.Hour)
	c := &Chat{ID: "example-thread", Source: "example", SourceID: "thread", Name: "Example", Messages: 1}
	// An older raw ID could itself look like the new hash prefix. New files need
	// a separate directory, not just a different basename in the legacy directory.
	name := attachmentName("asset/a", 0, "new.png", "IMAGE")
	rel := filepath.Join("attachments", at.In(s.Zone()).Format("2006-01-02"), name)
	old := filepath.Join(s.path(KindChat, c.ID), rel)
	if err := archiveWrite(old, []byte("legacy hash-shaped attachment")); err != nil {
		t.Fatal(err)
	}
	if err := s.AppendMessages(c, []Line{{At: at, Sender: "Example", Text: "Legacy picture " + attachmentPlaceholder + "(" + rel + ")"}}); err != nil {
		t.Fatal(err)
	}
	if err := s.PutChat(c); err != nil {
		t.Fatal(err)
	}
	src := &identityAttachmentSource{named: "example"}
	if n, err := e.Ingest(context.Background(), src, SourceChat{ID: "thread"}, []SourceMessage{{
		ID: "asset/a", At: at, Text: "New picture", Type: "IMAGE", Media: []string{"new.png"},
	}}); n != 1 || err != nil {
		t.Fatalf("new delivery: %d, %v", n, err)
	}
	assertAttachmentBytes(t, old, "legacy hash-shaped attachment")
	links := attachmentLinks(s, c.ID)
	if len(links) != 2 || links[0] != old || strings.EqualFold(links[0], links[1]) {
		t.Fatalf("legacy filename reused: %v", links)
	}
	assertAttachmentBytes(t, links[1], "new.png")
}

func TestMissingMessageIDsKeepSeparateAttachments(t *testing.T) {
	s, e := replayStore(t)
	src := &identityAttachmentSource{named: "example"}
	at := time.Now().Add(-time.Hour)
	chat := SourceChat{ID: "thread", Name: "Example"}
	msgs := []SourceMessage{
		{At: at, Position: "same", Text: "Anonymous picture", Type: "IMAGE", Media: []string{"first.png"}},
		{At: at, Position: "same", Text: "Anonymous picture", Type: "IMAGE", Media: []string{"second.png"}},
	}
	if n, err := e.Ingest(context.Background(), src, chat, msgs); n != 2 || err != nil {
		t.Fatalf("missing IDs: %d, %v", n, err)
	}
	s, err := NewStore(s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	e = &Engine{Store: s}
	if n, err := e.Ingest(context.Background(), src, chat, msgs[:1]); n != 1 || err != nil {
		t.Fatalf("legacy append retry: %d, %v", n, err)
	}
	links := attachmentLinks(s, "example-thread")
	if len(links) != 3 || strings.EqualFold(links[0], links[1]) || strings.EqualFold(links[0], links[2]) || strings.EqualFold(links[1], links[2]) {
		t.Fatalf("missing IDs reused attachment paths: %v", links)
	}
	for i, want := range []string{"first.png", "second.png", "first.png"} {
		assertAttachmentBytes(t, links[i], want)
	}
	if len(src.asked) != 3 {
		t.Fatalf("missing-ID append downloads: %v", src.asked)
	}
	if r, err := s.receipts("example", "thread", "example-thread"); err != nil || len(r.IDs) != 0 {
		t.Fatalf("invented a missing message identity: %+v, %v", r, err)
	}
	assertArchiveCounts(t, s, "example-thread", 3, 3)
	if n := s.Prune(time.Now().AddDate(0, 0, 2), 1, 1); n != 2 {
		t.Fatalf("attachment/text retention removed %d directories or days, want 2", n)
	}
	for _, p := range links {
		if _, err := os.Stat(p); !os.IsNotExist(err) {
			t.Fatalf("expired attachment remains: %v", err)
		}
	}
}
