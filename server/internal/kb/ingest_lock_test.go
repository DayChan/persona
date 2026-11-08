//go:build darwin || linux

package kb

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"testing"
	"time"
)

// The FIFO pauses TopTags' metadata read inside the actual project commit, before
// projectInput reads its timeline. Stack inspection is only a scheduling barrier:
// both lock orders block Ingest on metaMu, but only one leaves mu available to Log.
func TestIngestAndProjectCommitProgressWithOneLockOrder(t *testing.T) {
	for _, recoverPending := range []bool{false, true} {
		t.Run(map[bool]string{false: "ingest", true: "recovery"}[recoverPending], func(t *testing.T) {
			s, e := replayStore(t)
			m := SourceMessage{ID: "one", At: time.Now().Add(-time.Hour), Text: "Synthetic message"}
			if recoverPending {
				s.ingestWrite = func(path string, raw []byte) error {
					if filepath.Base(path) == "meta.json" {
						return os.ErrPermission
					}
					return archiveWrite(path, raw)
				}
				if _, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, []SourceMessage{m}); err == nil {
					t.Fatal("metadata failure not exercised")
				}
				s.ingestWrite = nil
			}
			p := &Project{Slug: "example", Name: "Example project", Dirty: true, Goal: "Keep the original goal",
				Tagging: Tagging{Pinned: "Keep this fact", LockedFields: []string{"goal"}}}
			if err := s.PutProject(p); err != nil {
				t.Fatal(err)
			}
			p, _ = s.Project(p.Slug)
			input := e.projectInput(p)
			fifo := s.path(KindChat, "pause", "meta.json")
			if err := os.MkdirAll(filepath.Dir(fifo), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := syscall.Mkfifo(fifo, 0o600); err != nil {
				t.Fatal(err)
			}
			projectDone := make(chan error, 1)
			go func() {
				projectDone <- e.commitProject(p, input, projectDigest{Summary: "Updated synthetic summary", Goal: "A model replacement"})
			}()
			var writer *os.File
			deadline := time.Now().Add(5 * time.Second)
			for writer == nil {
				f, err := os.OpenFile(fifo, os.O_WRONLY|syscall.O_NONBLOCK, 0o600)
				if err == nil {
					writer = f
					break
				}
				select {
				case err := <-projectDone:
					t.Fatalf("project commit did not reach its metadata barrier: %v", err)
				default:
				}
				if time.Now().After(deadline) {
					t.Fatal("project input did not reach its metadata barrier")
				}
				time.Sleep(time.Millisecond)
			}
			defer writer.Close()
			ingestDone := make(chan error, 1)
			go func() {
				_, err := e.Ingest(context.Background(), named("example"), SourceChat{ID: "thread"}, []SourceMessage{m})
				ingestDone <- err
			}()
			deadline = time.Now().Add(5 * time.Second)
			for {
				stack := make([]byte, 1<<20)
				n := runtime.Stack(stack, true)
				waiting := false
				for _, goroutine := range strings.Split(string(stack[:n]), "\n\n") {
					if strings.Contains(goroutine, "(*Store).recoverIngest(") && strings.Contains(goroutine, "[sync.Mutex.Lock]") {
						waiting = true
						break
					}
				}
				if waiting {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("ingestion did not reach its metadata lock barrier")
				}
				time.Sleep(time.Millisecond)
			}
			if _, err := writer.Write([]byte(`{}`)); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			for _, done := range []<-chan error{projectDone, ingestDone} {
				select {
				case err := <-done:
					if err != nil {
						t.Fatal(err)
					}
				case <-time.After(5 * time.Second):
					t.Fatal("project commit and ingestion could not progress through the metadata/day locks")
				}
			}
			assertArchiveCounts(t, s, "example-thread", 1, 1)
			cur, _ := s.Project(p.Slug)
			if cur.Dirty || cur.Goal != p.Goal || cur.Pinned != p.Pinned || !strings.Contains(s.Summary(KindProject, p.Slug), "Updated synthetic summary") {
				t.Fatalf("project guards or commit changed: %+v", cur)
			}
		})
	}
}
