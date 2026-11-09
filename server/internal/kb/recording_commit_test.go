package kb

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRejectedRecordingRevisionKeepsAcceptedTranscript(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Now().UTC()
	if status, err := e.PutRecording(recID, recUpload(1, now, true, "accepted transcript"), now); err != nil || status != RecordingStored {
		t.Fatalf("seed: %s %v", status, err)
	}
	// Prevent only the final metadata write. Both accepted metadata and transcript
	// remain readable, and the production file operations supply the error.
	if err := os.Mkdir(e.Store.path(KindRecording, recID, "meta.json.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	status, err := e.PutRecording(recID, recUpload(2, now, true, "rejected replacement"), now.Add(time.Second))
	if err == nil {
		t.Fatalf("update unexpectedly succeeded: %s", status)
	}
	t.Logf("revision 2 rejected: %v", err)
	r, _ := e.Store.Recording(recID)
	segs := e.Store.RecordingSegments(recID)
	detail, getErr := e.Get(KindRecording, recID, 1000)
	if getErr != nil {
		t.Fatal(getErr)
	}
	t.Logf("accepted metadata revision=%d, segments=%v, detail transcript=%q", r.Revision, segs, detail.Recent)
	if r.Revision != 1 || len(segs) != 1 || segs[0].Text != "accepted transcript" || !strings.Contains(detail.Recent, "accepted transcript") {
		t.Errorf("a rejected update replaced the accepted transcript while metadata still reports revision %d", r.Revision)
	}
	if hits := e.Find("rejected replacement", 10); len(hits) != 0 {
		t.Errorf("knowledge search exposes rejected revision: %v", hits)
	}
}

// Every production replacement is exercised both before and after it reaches
// disk. Panics model termination: no successful retry or rollback runs first.
func TestRecordingWriteBoundaries(t *testing.T) {
	for _, existing := range []bool{false, true} {
		for _, boundary := range []string{recordingJournal, "segments.json", "transcript.md", "meta.json", "commit"} {
			for _, after := range []bool{false, true} {
				if boundary == "commit" && after {
					continue
				} // removal is the commit point
				for _, crash := range []bool{false, true} {
					t.Run(fmt.Sprintf("existing=%t/%s/after=%t/crash=%t", existing, boundary, after, crash), func(t *testing.T) {
						e := goldenEngine(t, nil)
						now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
						if existing {
							mustStoreRecording(t, e, recID, recUpload(1, now, true, "accepted orchard"), now)
						}
						before, err := e.Store.recordingDiskSnapshot(recID)
						if err != nil {
							t.Fatal(err)
						}
						// Warm both search caches before attempting the rejected replacement.
						e.Find("accepted orchard", 10)
						e.Search("accepted orchard", true, 10)
						sentinel := errors.New("synthetic recording write failure")
						trip := func() error {
							if crash {
								panic(sentinel)
							}
							return sentinel
						}
						e.Store.recordingWrite = func(path string, raw []byte) error {
							if filepath.Base(path) == boundary && !after {
								return trip()
							}
							if err := archiveWrite(path, raw); err != nil {
								return err
							}
							if filepath.Base(path) == boundary && after {
								return trip()
							}
							return nil
						}
						e.Store.recordingRemove = func(path string) error {
							if boundary == "commit" {
								return trip()
							}
							return os.Remove(path)
						}
						var notifications, indexes int
						e.Store.changed = func(Change) { notifications++ }
						e.Index = func(string, string, string, Tagging, string, time.Time) { indexes++ }
						func() {
							defer func() {
								if v := recover(); v != nil && (!crash || v != sentinel) {
									panic(v)
								}
							}()
							_, err := e.PutRecording(recID, recUpload(2, now, true, "rejected zeppelin"), now)
							if crash {
								t.Fatal("expected simulated interruption")
							}
							if !errors.Is(err, sentinel) {
								t.Fatalf("write error = %v", err)
							}
						}()
						if notifications != 0 || indexes != 0 {
							t.Fatalf("rejected write published: %d notifications, %d indexes", notifications, indexes)
						}
						assertRecordingView(t, e, existing, "accepted orchard", "rejected zeppelin")
						// OpenReadOnly must never expose an unresolved multi-file replacement.
						if _, err := os.Stat(e.Store.path(KindRecording, recID, recordingJournal)); err == nil {
							if _, err := OpenReadOnly(e.Store.Dir); err == nil {
								t.Fatal("read-only open accepted an unresolved journal")
							}
						}
						reopened, err := NewStore(e.Store.Dir)
						if err != nil {
							t.Fatal(err)
						}
						e.Store = reopened
						assertRecordingView(t, e, existing, "accepted orchard", "rejected zeppelin")
						disk, err := reopened.recordingDiskSnapshot(recID)
						if err != nil || !reflect.DeepEqual(before, disk) {
							t.Fatalf("accepted bytes changed on reopen: %v", err)
						}
						// The same revision is retryable after recovery, including first uploads.
						e.Index = nil
						mustStoreRecording(t, e, recID, recUpload(2, now, true, "rejected zeppelin"), now)
						assertRecordingView(t, e, true, "rejected zeppelin", "accepted orchard")
						if status, err := e.PutRecording(recID, recUpload(1, now, true, "stale text"), now); err != nil || status != RecordingStale {
							t.Fatalf("stale = %s, %v", status, err)
						}
						mustStoreRecording(t, e, recID, recUpload(2, now, true, "rejected zeppelin"), now)
					})
				}
			}
		}
	}
}

func mustStoreRecording(t *testing.T, e *Engine, id string, u RecordingUpload, now time.Time) {
	t.Helper()
	if status, err := e.PutRecording(id, u, now); err != nil || status != RecordingStored {
		t.Fatalf("store = %s, %v", status, err)
	}
}

func assertRecordingView(t *testing.T, e *Engine, exists bool, accepted, rejected string) {
	t.Helper()
	r, ok := e.Store.Recording(recID)
	segs := e.Store.RecordingSegments(recID)
	detail, err := e.Get(KindRecording, recID, 10000)
	if ok != exists {
		t.Fatalf("exists = %t, want %t", ok, exists)
	}
	if exists {
		if len(segs) != 1 || segs[0].Text != accepted || err != nil || len(detail.Segments) != 1 || detail.Segments[0].Text != accepted || !strings.Contains(detail.Recent, accepted) || strings.Contains(detail.Recent, rejected) {
			t.Fatalf("accepted view: meta=%+v, segments=%+v, detail=%+v, err=%v", r, segs, detail, err)
		}
		var meta Recording
		if err := json.Unmarshal(detail.Meta, &meta); err != nil || meta.Revision != r.Revision {
			t.Fatalf("detail metadata: %+v %v", meta, err)
		}
		if len(e.Find(accepted, 10)) != 1 || len(e.Search(accepted, true, 10)) == 0 || len(e.Matches(KindRecording, recID, accepted, 10)) == 0 {
			t.Fatal("accepted text missing from search")
		}
	} else if !errors.Is(err, ErrNotFound) || len(segs) != 0 {
		t.Fatalf("unaccepted first upload visible: %+v, %v", detail, err)
	}
	for _, hit := range e.Search(accepted, true, 10) {
		if strings.Contains(hit.Path, recordingJournal) || strings.HasSuffix(hit.Path, ".tmp") {
			t.Fatalf("bookkeeping became searchable: %s", hit.Path)
		}
	}
	if len(e.Find(rejected, 10)) != 0 || len(e.Search(rejected, true, 10)) != 0 || len(e.Matches(KindRecording, recID, rejected, 10)) != 0 {
		t.Fatal("rejected text leaked to search/model context")
	}
}

func TestRecordingConcurrentRevisionsAndReaders(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	ids := []string{recID, "22222222-2222-4222-8222-222222222222"}
	for _, id := range ids {
		mustStoreRecording(t, e, id, recUpload(1, now, false, "revision 1"), now)
	}
	var wg sync.WaitGroup
	errs := make(chan error, 100)
	for _, id := range ids {
		for rev := int64(2); rev <= 12; rev++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := e.PutRecording(id, recUpload(rev, now, false, fmt.Sprintf("revision %d", rev)), now)
				if err != nil {
					errs <- err
				}
			}()
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 30 {
				d, err := e.Get(KindRecording, id, 10000)
				if err != nil {
					errs <- err
					continue
				}
				var meta Recording
				if err := json.Unmarshal(d.Meta, &meta); err != nil {
					errs <- err
					continue
				}
				want := fmt.Sprintf("revision %d", meta.Revision)
				if len(d.Segments) != 1 || d.Segments[0].Text != want || !strings.Contains(d.Recent, want+"\n") {
					errs <- fmt.Errorf("mixed revision: %s %+v", d.Meta, d)
				}
				e.Find("revision", 10)
				e.Search("revision", true, 10)
				e.Matches(KindRecording, id, "revision", 10)
				_ = e.Store.RecordingSegments(id)
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Error(err)
	}
	for _, id := range ids {
		r, _ := e.Store.Recording(id)
		if r.Revision != 12 {
			t.Fatalf("final revision = %d", r.Revision)
		}
	}
}

func TestRecordingRejectedRevisionModelInputAndLegacyEdits(t *testing.T) {
	model := &captureModel{reply: `{"title":"Synthetic title","summary":"Synthetic summary"}`}
	e := goldenEngine(t, model)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if err := e.Store.PutProject(&Project{Slug: "synthetic-project", Name: "Synthetic project", Status: "active", Items: []string{KindRecording + "/" + recID}}); err != nil {
		t.Fatal(err)
	}
	// Seed the historical layout directly, without any journal or new helper.
	r := &Recording{ID: recID, Revision: 1, StartedAt: now, Device: "phone", Name: "Synthetic recording", Chars: 16, Segments: 1, Complete: true, Projects: []string{"synthetic-project"}, Tagging: Tagging{Pinned: "Keep this fact", LockedFields: []string{"tags"}, Tags: []string{"kept"}}}
	for name, value := range map[string]any{"meta.json": r, "segments.json": []RecordingSegment{{Text: "accepted orchard"}}} {
		if err := writeJSON(e.Store.path(KindRecording, recID, name), value); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeFile(e.Store.path(KindRecording, recID, "transcript.md"), []byte("accepted orchard\n")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(e.Store.path(KindRecording, recID, "transcript.md.tmp"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := e.PutRecording(recID, recUpload(2, now, true, "rejected zeppelin"), now); err == nil {
		t.Fatal("expected write failure")
	}
	assertRecordingView(t, e, true, "accepted orchard", "rejected zeppelin")
	if n := e.SummarizeRecordings(context.Background(), 1); n != 1 {
		t.Fatalf("summaries = %d", n)
	}
	if len(model.calls) != 1 || !strings.Contains(model.calls[0][1], "accepted orchard") || strings.Contains(model.calls[0][1], "rejected zeppelin") {
		t.Fatalf("model input = %+v", model.calls)
	}
	if err := os.Remove(e.Store.path(KindRecording, recID, "transcript.md.tmp")); err != nil {
		t.Fatal(err)
	}
	u := recUpload(2, now, false, "accepted orchard", "appended synthetic pear")
	u.Bookmarks = []RecordingBookmark{{At: 2, Label: "Synthetic bookmark"}}
	mustStoreRecording(t, e, recID, u, now)
	got, _ := e.Store.Recording(recID)
	if got.Pinned != "Keep this fact" || !reflect.DeepEqual(got.Tags, []string{"kept"}) || !got.fieldLocked("tags") || !reflect.DeepEqual(got.Projects, []string{"synthetic-project"}) {
		t.Fatalf("protected fields lost: %+v", got)
	}
	if project, _ := e.Store.Project("synthetic-project"); project == nil || !reflect.DeepEqual(project.Items, []string{KindRecording + "/" + recID}) {
		t.Fatalf("recording relationship changed: %+v", project)
	}
	d, err := e.Get(KindRecording, recID, 10000)
	if err != nil || len(d.Segments) != 2 || !strings.Contains(d.Recent, "Synthetic bookmark") || !strings.Contains(d.Recent, "appended synthetic pear") {
		t.Fatalf("append/bookmark: %+v %v", d, err)
	}
	physical, err := os.ReadFile(e.Store.path(KindRecording, recID, "transcript.md"))
	if err != nil || !bytes.Equal(physical, []byte(d.Recent)) {
		t.Fatalf("legacy transcript link: %v", err)
	}
}

func TestRecordingProcessRestart(t *testing.T) {
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	if root := os.Getenv("PERSONA_RECORDING_CRASH_DIR"); root != "" {
		st, err := NewStore(root)
		if err != nil {
			t.Fatal(err)
		}
		e := &Engine{Store: st}
		boundary := os.Getenv("PERSONA_RECORDING_CRASH_BOUNDARY")
		st.recordingWrite = func(path string, raw []byte) error {
			if err := archiveWrite(path, raw); err != nil {
				return err
			}
			if filepath.Base(path) == boundary {
				os.Exit(81)
			}
			return nil
		}
		st.recordingRemove = func(path string) error {
			if err := os.Remove(path); err != nil {
				return err
			}
			if boundary == "committed" {
				os.Exit(81)
			}
			return nil
		}
		_, err = e.PutRecording(recID, recUpload(2, now, true, "candidate zeppelin"), now)
		t.Fatalf("child did not interrupt: %v", err)
	}
	for _, boundary := range []string{recordingJournal, "segments.json", "transcript.md", "meta.json", "committed"} {
		t.Run(boundary, func(t *testing.T) {
			e := goldenEngine(t, nil)
			mustStoreRecording(t, e, recID, recUpload(1, now, true, "accepted orchard"), now)
			cmd := exec.Command(os.Args[0], "-test.run=^TestRecordingProcessRestart$")
			cmd.Env = append(os.Environ(), "PERSONA_RECORDING_CRASH_DIR="+e.Store.Dir, "PERSONA_RECORDING_CRASH_BOUNDARY="+boundary)
			out, err := cmd.CombinedOutput()
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 81 {
				t.Fatalf("child exit = %v: %s", err, out)
			}
			st, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			e.Store = st
			if boundary == "committed" {
				assertRecordingView(t, e, true, "candidate zeppelin", "accepted orchard")
			} else {
				assertRecordingView(t, e, true, "accepted orchard", "candidate zeppelin")
			}
		})
	}
}

func TestRecordingBlockedRecoveryKeepsAcceptedView(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	mustStoreRecording(t, e, recID, recUpload(1, now, true, "accepted orchard"), now)
	e.Store.recordingWrite = func(path string, raw []byte) error {
		if filepath.Base(path) == "transcript.md" {
			// A directory at a replacement target also blocks undo. The accepted bytes
			// remain solely in the journal until the storage obstruction is repaired.
			segments := e.Store.path(KindRecording, recID, "segments.json")
			if err := os.Remove(segments); err != nil {
				return err
			}
			if err := os.Mkdir(segments, 0o700); err != nil {
				return err
			}
			return errors.New("synthetic storage obstruction")
		}
		return archiveWrite(path, raw)
	}
	if _, err := e.PutRecording(recID, recUpload(2, now, true, "rejected zeppelin"), now); err == nil {
		t.Fatal("expected failure")
	}
	assertRecordingView(t, e, true, "accepted orchard", "rejected zeppelin")
	if _, err := NewStore(e.Store.Dir); err == nil {
		t.Fatal("writable open ignored failed recovery")
	}
	// Ownership edits must not invalidate the recovery snapshot while blocked.
	if _, err := e.Apply(KindRecording, recID, Update{Tags: []string{"synthetic"}}); err == nil {
		t.Fatal("edit admitted before recovery")
	}
	assertRecordingView(t, e, true, "accepted orchard", "rejected zeppelin")
	if err := os.Remove(e.Store.path(KindRecording, recID, "segments.json")); err != nil {
		t.Fatal(err)
	}
	st, err := NewStore(e.Store.Dir)
	if err != nil {
		t.Fatal(err)
	}
	e.Store = st
	assertRecordingView(t, e, true, "accepted orchard", "rejected zeppelin")
	if _, err := e.Apply(KindRecording, recID, Update{Tags: []string{"synthetic"}}); err != nil {
		t.Fatal(err)
	}
	mustStoreRecording(t, e, recID, recUpload(2, now, true, "healthy retry"), now)
}

func TestRecordingRealTemporaryFileFailures(t *testing.T) {
	for _, name := range []string{"segments.json", "transcript.md", "meta.json"} {
		t.Run(name, func(t *testing.T) {
			e := goldenEngine(t, nil)
			now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			mustStoreRecording(t, e, recID, recUpload(1, now, true, "accepted orchard"), now)
			if err := os.Mkdir(e.Store.path(KindRecording, recID, name+".tmp"), 0o700); err != nil {
				t.Fatal(err)
			}
			if _, err := e.PutRecording(recID, recUpload(2, now, true, "rejected zeppelin"), now); err == nil {
				t.Fatal("expected real filesystem failure")
			}
			assertRecordingView(t, e, true, "accepted orchard", "rejected zeppelin")
			st, err := NewStore(e.Store.Dir)
			if err != nil {
				t.Fatal(err)
			}
			e.Store = st
			assertRecordingView(t, e, true, "accepted orchard", "rejected zeppelin")
			if err := os.Remove(st.path(KindRecording, recID, name+".tmp")); err != nil {
				t.Fatal(err)
			}
			mustStoreRecording(t, e, recID, recUpload(2, now, true, "healthy retry"), now)
		})
	}
}

func TestRecordingPausedCommitReaders(t *testing.T) {
	for _, boundary := range []string{recordingJournal, "segments.json", "transcript.md", "meta.json", "commit"} {
		for _, fail := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/fail=%t", boundary, fail), func(t *testing.T) {
				e := goldenEngine(t, nil)
				now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
				seed := recUpload(1, now, true, "accepted orchard coherent-marker")
				seed.Title = "Accepted orchard"
				mustStoreRecording(t, e, recID, seed, now)
				// Populate Find's entry cache too; equal-length metadata must not alias
				// physical stats belonging to an unfinished generation.
				e.Find("accepted orchard coherent-marker", 10)
				parked, release := make(chan struct{}), make(chan struct{})
				var once sync.Once
				pause := func() error {
					once.Do(func() { close(parked) })
					<-release
					if fail {
						return errors.New("synthetic paused write failure")
					}
					return nil
				}
				e.Store.recordingWrite = func(path string, raw []byte) error {
					if err := archiveWrite(path, raw); err != nil {
						return err
					}
					if filepath.Base(path) == boundary {
						return pause()
					}
					return nil
				}
				e.Store.recordingRemove = func(path string) error {
					if boundary == "commit" {
						if err := pause(); err != nil {
							return err
						}
					}
					return os.Remove(path)
				}
				candidate := recUpload(2, now, true, "candidate zeppelin coherent-marker")
				candidate.Title = "Candidate zeppelin"
				written := make(chan error, 1)
				go func() { _, err := e.PutRecording(recID, candidate, now); written <- err }()
				select {
				case <-parked:
				case <-time.After(10 * time.Second):
					t.Fatal("writer never reached boundary")
				}
				defer releaseRecordingGate(release)
				// A shared transcript token keeps bounded legacy search focused on the
				// recording; broad single-letter queries can fill the hit limit
				// with README and metadata lines before reaching the transcript.
				readers := []func() any{
					func() any {
						d, err := e.Get(KindRecording, recID, 10000)
						if err != nil {
							return err
						}
						return d
					},
					func() any { return e.Find("coherent-marker", 10) },
					func() any { return e.Search("coherent-marker", true, 10) },
					func() any { return e.Matches(KindRecording, recID, "coherent-marker", 10) },
					func() any { return e.Store.RecordingSegments(recID) },
					func() any { kept, _ := e.RecordingStates(); return kept },
				}
				entered := make(chan struct{}, len(readers))
				e.Store.recordingReadWait = func() { entered <- struct{}{} }
				results := make(chan any, len(readers))
				for _, read := range readers {
					go func() { results <- read() }()
				}
				for range readers {
					select {
					case <-entered:
					case <-time.After(10 * time.Second):
						t.Fatal("reader did not enter accepted-view gate")
					}
				}
				select {
				case got := <-results:
					t.Fatalf("reader bypassed paused replacement: %+v", got)
				default:
				}
				close(release)
				err := <-written
				if (err != nil) != fail {
					t.Fatalf("write error = %v, fail=%t", err, fail)
				}
				for range readers {
					select {
					case got := <-results:
						if err, ok := got.(error); ok {
							t.Fatalf("reader failed after resuming: %v", err)
						}
						wantText, wantTitle, wantRev := "candidate zeppelin coherent-marker", "Candidate zeppelin", int64(2)
						if fail {
							wantText, wantTitle, wantRev = "accepted orchard coherent-marker", "Accepted orchard", 1
						}
						switch value := got.(type) {
						case *Detail:
							var meta Recording
							if err := json.Unmarshal(value.Meta, &meta); err != nil || meta.Revision != wantRev || len(value.Segments) != 1 || value.Segments[0].Text != wantText || !strings.Contains(value.Recent, wantText) {
								t.Fatalf("incoherent resumed detail: %+v", value)
							}
						case []Result:
							if len(value) != 1 || value[0].Entry.Name != wantTitle {
								t.Fatalf("incoherent resumed find: %+v", value)
							}
						case []Hit:
							found := false
							for _, hit := range value {
								found = found || strings.Contains(hit.Line, wantText)
							}
							if !found {
								t.Fatalf("resumed search omitted accepted text: %+v", value)
							}
						case []Match:
							found := false
							for _, match := range value {
								found = found || strings.Contains(match.Line, wantText)
							}
							if !found {
								t.Fatalf("resumed matches omitted accepted text: %+v", value)
							}
						case []RecordingSegment:
							if len(value) != 1 || value[0].Text != wantText {
								t.Fatalf("incoherent resumed segments: %+v", value)
							}
						case []RecordingState:
							if len(value) != 1 || value[0].Revision != wantRev {
								t.Fatalf("incoherent resumed reconciliation: %+v", value)
							}
						default:
							t.Fatalf("unexpected reader result: %T", got)
						}
						raw, err := json.Marshal(got)
						if err != nil {
							t.Fatal(err)
						}
						if (fail && (strings.Contains(string(raw), "zeppelin") || strings.Contains(string(raw), `"revision":2`))) || (!fail && (strings.Contains(string(raw), "orchard") || strings.Contains(string(raw), `"revision":1`))) {
							t.Fatalf("rejected content visible: %s", raw)
						}
					case <-time.After(10 * time.Second):
						t.Fatal("reader did not resume")
					}
				}
				e.Store.recordingReadWait = nil
				if fail {
					assertRecordingView(t, e, true, "accepted orchard coherent-marker", "candidate zeppelin coherent-marker")
				} else {
					assertRecordingView(t, e, true, "candidate zeppelin coherent-marker", "accepted orchard coherent-marker")
				}
			})
		}
	}
}

func releaseRecordingGate(ch chan struct{}) {
	select {
	case <-ch:
	default:
		close(ch)
	}
}

func TestRecordingObserversReadCommittedContent(t *testing.T) {
	e := goldenEngine(t, nil)
	now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
	var calls int
	e.Changed = func(change Change) {
		if change.Kind != KindRecording {
			return
		}
		calls++
		detail, err := e.Get(KindRecording, recID, 10000)
		if err != nil || len(detail.Segments) != 1 || detail.Segments[0].Text != "accepted orchard" {
			t.Errorf("observer saw unfinished content: %+v %v", detail, err)
		}
	}
	e.ObserveChanges()
	mustStoreRecording(t, e, recID, recUpload(1, now, false, "accepted orchard"), now)
	if _, err := e.Apply(KindRecording, recID, Update{Tags: []string{"synthetic"}}); err != nil {
		t.Fatal(err)
	}
	if calls < 3 {
		t.Fatalf("notifications = %d", calls)
	}
}

// The journal encodes transcripts but exposes plaintext identity/schema keys.
// Search must exclude the file itself, including a first upload with no accepted
// metadata and a failed restoration that leaves the journal in place.
func TestRecordingSearchSkipsPendingUndo(t *testing.T) {
	for _, existing := range []bool{false, true} {
		t.Run(fmt.Sprintf("existing=%t", existing), func(t *testing.T) {
			e := goldenEngine(t, nil)
			now := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			if existing {
				mustStoreRecording(t, e, recID, recUpload(1, now, true, "accepted orchard"), now)
			}
			e.Store.recordingWrite = func(path string, raw []byte) error {
				if filepath.Base(path) == "transcript.md" {
					// Block rollback at an actual filesystem replacement boundary.
					segments := e.Store.path(KindRecording, recID, "segments.json")
					if err := os.Remove(segments); err != nil {
						return err
					}
					if err := os.Mkdir(segments, 0o700); err != nil {
						return err
					}
					// Removing an empty directory would recover a first upload;
					// keep it nonempty so both old and absent files stay blocked.
					if err := writeFile(filepath.Join(segments, "blocked"), []byte("synthetic obstruction")); err != nil {
						return err
					}
					return errors.New("synthetic pending recording recovery")
				}
				return archiveWrite(path, raw)
			}
			if _, err := e.PutRecording(recID, recUpload(2, now, true, "rejected zeppelin"), now); err == nil {
				t.Fatal("expected upload and restoration failure")
			}
			journal := e.Store.path(KindRecording, recID, recordingJournal)
			raw, err := os.ReadFile(journal)
			if err != nil || !strings.Contains(string(raw), recID) || !strings.Contains(string(raw), `"files"`) {
				t.Fatalf("pending journal fixture missing: %v", err)
			}
			for _, messages := range []bool{false, true} {
				for _, query := range []string{recID, `"version"`, `"files"`, `"id"`} {
					hits := e.Search(query, messages, 20)
					for _, hit := range hits {
						if filepath.Base(hit.Path) == recordingJournal {
							t.Errorf("Search(%q,%t) exposed undo bookkeeping: %+v", query, messages, hit)
						}
					}
					if !existing && query == recID && len(hits) != 0 {
						t.Errorf("first rejected upload became searchable by UUID: %+v", hits)
					}
					if existing && query == recID && len(hits) == 0 {
						t.Error("accepted legacy metadata stopped matching its UUID")
					}
				}
			}
			// Exact artifact exclusion keeps ordinary user-authored JSON and Markdown
			// under a recording searchable through the legacy API.
			for _, file := range []struct{ name, text string }{
				{"notes.json", `{"files":"synthetic legacy json marker"}`},
				{"notes.md", "synthetic legacy markdown marker"},
			} {
				if err := writeFile(e.Store.path(KindRecording, recID, file.name), []byte(file.text)); err != nil {
					t.Fatal(err)
				}
				query := "synthetic legacy json marker"
				if file.name == "notes.md" {
					query = "synthetic legacy markdown marker"
				}
				hits := e.Search(query, true, 20)
				if len(hits) != 1 || filepath.Base(hits[0].Path) != file.name {
					t.Fatalf("legacy %s search changed: %+v", file.name, hits)
				}
			}
			if _, err := NewStore(e.Store.Dir); err == nil {
				t.Fatal("blocked recovery unexpectedly succeeded")
			}
			for _, hit := range e.Search(recID, true, 20) {
				if filepath.Base(hit.Path) == recordingJournal {
					t.Fatalf("failed reopen exposed journal: %+v", hit)
				}
			}
		})
	}
}
