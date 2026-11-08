package kb

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadOnlyStoreRefusesWritesAndUsesArchivedZone(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KindChat, "chat", "messages")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{".zone": "Asia/Tokyo", "2026-11-01.md": "- 23:30 Example\n"} {
		if err := os.WriteFile(filepath.Join(path, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	s, err := OpenReadOnly(dir)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 1, 14, 30, 0, 0, time.UTC)
	if got := s.FirstAt("chat"); !got.Equal(want) {
		t.Fatalf("archive zone: got %v want %v", got, want)
	}
	if got := s.Since("chat", want.Add(time.Minute), 1000); got != "" {
		t.Fatalf("Since included an older line: %s", got)
	}
	for _, write := range []func() error{
		func() error { return s.PutChat(&Chat{ID: "new"}) },
		func() error { return s.PutPerson(&Person{ID: "new"}) },
		func() error { return s.PutProject(&Project{Slug: "new"}) },
		func() error { return s.PutItem(&Item{Kind: KindDoc, ID: "new"}) },
		func() error { return s.PutSummary(KindChat, "chat", "new") },
		func() error { return s.AppendLog("new", time.Now(), "source", "line") },
		func() error { return s.WriteLog("new", []string{"line"}) },
		func() error { return s.AppendMessages(&Chat{ID: "chat"}, nil) },
		func() error { _, err := s.MergeMessages(&Chat{ID: "chat"}, nil); return err },
		func() error { return s.PutHistoryState("chat", HistoryState{Mode: HistoryFull}) },
		func() error {
			_, err := (&Engine{Store: s}).WriteHistory(context.Background(), &Chat{ID: "chat"}, nil, HistoryWrite{})
			return err
		},
		func() error { return s.PutMomentPost(&MomentPost{ID: "new"}, nil) },
		func() error { _, err := s.WriteMomentIndex(); return err },
		func() error { _, err := s.Rezone(time.UTC); return err },
	} {
		if err := write(); !errors.Is(err, ErrReadOnly) {
			t.Fatalf("write returned %v", err)
		}
	}
	if s.Prune(time.Now().AddDate(10, 0, 0), 1, 1) != 0 || s.PruneMoments(time.Now()) != 0 {
		t.Fatal("read-only prune removed content")
	}
	if _, err := os.Stat(filepath.Join(path, "2026-11-01.md")); err != nil {
		t.Fatal(err)
	}
}

func TestReadOnlyOpenRejectsInvalidArchive(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("data"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(file); err == nil {
		t.Fatal("opened a file as an empty archive")
	}
	if err := os.Symlink(file, filepath.Join(dir, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(dir); err == nil {
		t.Fatal("opened an archive with a symbolic link")
	}
}

func TestReadOnlyOpenRejectsCorruptEntityMetadata(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, KindProject, "example")
	if err := os.MkdirAll(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "meta.json"), []byte(`{"relevance":"invalid"}`), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenReadOnly(dir); err == nil {
		t.Fatal("corrupt metadata was accepted as empty")
	}
}
