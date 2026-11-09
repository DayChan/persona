package node

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"
	"time"
)

// pruneSources are real (scrubbed) transcripts: one with background tasks still running (the start of
// claude-background.jsonl without the notifications that end them), one with a title and last words, and two with
// finished background tasks, in three working directories.
func pruneSources(t *testing.T) [][]byte {
	t.Helper()
	read := func(name string) []byte {
		b, err := os.ReadFile(filepath.Join("..", "transcript", "testdata", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	bg := read("claude-background.jsonl")
	var running []byte
	for _, line := range bytes.SplitAfter(bg, []byte("\n")) {
		if !bytes.Contains(line, []byte("<task-notification>")) {
			running = append(running, line...)
		}
	}
	return [][]byte{running, read("claude-compact.jsonl"), read("claude-notifications.jsonl"), bg}
}

// writePruneTranscripts writes count transcripts into dir, cycling through pruneSources, each a minute older than the last.
func writePruneTranscripts(t *testing.T, dir string, count int, newest time.Time) []string {
	t.Helper()
	srcs := pruneSources(t)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	paths := make([]string, count)
	for i := range paths {
		p := filepath.Join(dir, fmt.Sprintf("cccccccc-0000-0000-0000-%012d.jsonl", i))
		if err := os.WriteFile(p, srcs[i%len(srcs)], 0o600); err != nil {
			t.Fatal(err)
		}
		at := newest.Add(-time.Duration(i) * time.Minute) // each directory's newest transcript gives its time
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
		paths[i] = p
	}
	return paths
}

func heapInUse() int64 {
	var ms runtime.MemStats
	runtime.GC()
	runtime.GC()
	runtime.ReadMemStats(&ms)
	return int64(ms.HeapAlloc)
}

// A node read 1,000 transcripts that were then deleted: what it kept for each (the quick meta and the background-task
// scan) goes, all but one small entry per working directory, and the recent directories built from them stay exactly
// as they were.
func TestPruneGoneMetas(t *testing.T) {
	const count = 1000
	n := &Node{metas: map[string]quickMeta{}, bgScans: map[string]*bgScan{}}
	dir := filepath.Join(t.TempDir(), "projects", "-work-project")
	paths := writePruneTranscripts(t, dir, count+1, time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC))
	kept, paths := paths[count], paths[:count]

	start := heapInUse()
	for _, p := range append([]string{kept}, paths...) {
		st, err := os.Stat(p)
		if err != nil {
			t.Fatal(err)
		}
		n.meta(p, st)
	}
	if len(n.bgScans[paths[0]].meta.Background) == 0 || n.metas[paths[1]].lastText == "" || n.metas[paths[1]].title == "" {
		t.Fatal("the fixtures should give running background tasks, a title and last words")
	}
	for _, p := range paths {
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	before := n.dirsByUse()
	keptMeta := n.metas[kept]
	wantDirs := map[string]time.Time{} // each deleted transcript's directory, with its newest one's mtime
	for _, p := range paths {
		if m := n.metas[p]; m.cwd != "" && m.mtime.After(wantDirs[m.cwd]) {
			wantDirs[m.cwd] = m.mtime
		}
	}
	if len(wantDirs) < 2 {
		t.Fatalf("the fixtures should run in several directories: %v", wantDirs)
	}
	held := heapInUse() - start

	n.pruneGoneMetas(map[string]bool{kept: true})
	left := heapInUse() - start
	t.Logf("%d transcripts read, then deleted: the node held %.1f MB (%.1f KB each); after the prune it holds %.1f KB (%.0f B each)",
		count, float64(held)/(1<<20), float64(held)/count/1024, float64(left)/1024, float64(left)/count)

	if len(n.bgScans) != 1 || n.bgScans[kept] == nil {
		t.Fatalf("background-task scans left: %d, want only the existing transcript's", len(n.bgScans))
	}
	if len(n.metas) != 1 {
		t.Fatalf("metas: %d entries, want only the existing transcript's", len(n.metas))
	}
	if !reflect.DeepEqual(n.goneDirs, wantDirs) {
		t.Errorf("directories of the gone transcripts\n got %v\nwant %v", n.goneDirs, wantDirs)
	}
	if !reflect.DeepEqual(n.metas[kept], keptMeta) {
		t.Error("an existing transcript's meta changed")
	}
	if after := n.dirsByUse(); !reflect.DeepEqual(after, before) {
		t.Errorf("recent directories changed\n got %v\nwant %v", after, before)
	}

	// A transcript made again at a pruned path is read in full, its background tasks from the start.
	src, err := os.ReadFile(kept)
	if err != nil {
		t.Fatal(err)
	}
	back := paths[4*7] // the same source as kept: count is a multiple of the four sources
	if err := os.WriteFile(back, src, 0o600); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(back)
	if err != nil {
		t.Fatal(err)
	}
	m := n.meta(back, st)
	m.readAt, m.mtime = keptMeta.readAt, keptMeta.mtime
	if !reflect.DeepEqual(m, keptMeta) {
		t.Errorf("a transcript back at a pruned path\n got %+v\nwant %+v", m, keptMeta)
	}
	if sc := n.bgScans[back]; sc == nil || sc.off != st.Size() {
		t.Error("a transcript back at a pruned path should be scanned for background tasks again")
	}
}

// An entry read again between the snapshot and the re-lock is left alone (the file may be back by then); a gone one
// leaves the cache and is not stat'ed again, its directory kept.
func TestPruneGoneMetasKeepsEntriesReadMeanwhile(t *testing.T) {
	dir := t.TempDir()
	n := &Node{metas: map[string]quickMeta{}, bgScans: map[string]*bgScan{}}
	back, gone := filepath.Join(dir, "back.jsonl"), filepath.Join(dir, "gone.jsonl")
	at := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	for _, p := range []string{back, gone} {
		n.metas[p] = quickMeta{size: 10, cwd: "/work/project/f1", mtime: at, title: "old", readAt: time.Now()}
		n.bgScans[p] = &bgScan{}
	}
	check := n.pruneCandidates(nil)
	if len(check) != 2 {
		t.Fatalf("candidates: %v", check)
	}
	fresh := quickMeta{size: 20, cwd: "/work/project/f1", title: "new", readAt: time.Now().Add(time.Second)}
	n.metas[back] = fresh // what n.meta leaves when it reads the file again in between
	n.dropGoneMetas(check, []string{back, gone})
	if !reflect.DeepEqual(n.metas[back], fresh) || n.bgScans[back] == nil {
		t.Errorf("an entry read again was pruned: %+v", n.metas[back])
	}
	if m, ok := n.metas[gone]; ok || n.bgScans[gone] != nil {
		t.Errorf("gone entry not pruned: %+v", m)
	}
	if want := map[string]time.Time{"/work/project/f1": at}; !reflect.DeepEqual(n.goneDirs, want) {
		t.Errorf("directories of the gone transcripts: %v, want %v", n.goneDirs, want)
	}
	if check := n.pruneCandidates(nil); len(check) != 1 {
		t.Errorf("candidates after the prune: %v, want only the entry read again", check)
	}
}

// backgroundTasks forgets a transcript that was deleted, so a file made again at that path is scanned from its start.
func TestBackgroundTasksForgetsDeletedTranscript(t *testing.T) {
	src := pruneSources(t)[0]
	path := filepath.Join(t.TempDir(), "s.jsonl")
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	n := &Node{bgScans: map[string]*bgScan{}}
	n.backgroundTasks(path)
	if sc := n.bgScans[path]; sc == nil || len(sc.meta.Background) == 0 {
		t.Fatal("the fixture should have background tasks")
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	n.backgroundTasks(path)
	if len(n.bgScans) != 0 {
		t.Fatalf("a deleted transcript's scan stays: %d entries", len(n.bgScans))
	}
	if err := os.WriteFile(path, src, 0o600); err != nil {
		t.Fatal(err)
	}
	n.backgroundTasks(path)
	if sc := n.bgScans[path]; sc == nil || sc.off != int64(len(src)) || len(sc.meta.Background) == 0 {
		t.Fatal("a transcript made again is scanned from its start")
	}
}

// scanOffline prunes at most once a minute, and the transcripts it globs are not stat'ed for it.
func TestScanOfflinePrunesGoneMetas(t *testing.T) {
	n, _ := claudeHome(t)
	n.metas, n.bgScans = map[string]quickMeta{}, map[string]*bgScan{}
	dir := filepath.Join(n.home, ".claude", "projects", "-work-project")
	paths := writePruneTranscripts(t, dir, 8, time.Now()) // inside the offline window: listed, so read through n.meta
	if len(n.scanOffline()) == 0 {
		t.Fatal("the transcripts should be listed")
	}
	read := cachedMetas(n)
	deleted := 0
	for _, p := range paths[:4] {
		if _, ok := n.metas[p]; ok {
			deleted++
		}
		if err := os.Remove(p); err != nil {
			t.Fatal(err)
		}
	}
	if deleted == 0 {
		t.Fatal("none of the deleted transcripts was read (the test's premise)")
	}
	n.scanOffline()
	if cached := cachedMetas(n); cached != read {
		t.Fatalf("pruned again within a minute: %d metas, want %d", cached, read)
	}
	metaPruneAt.Lock()
	metaPruneAt.at = time.Time{}
	metaPruneAt.Unlock()
	n.scanOffline()
	if cached := cachedMetas(n); cached != read-deleted {
		t.Fatalf("%d metas after the prune, want %d", cached, read-deleted)
	}
	for _, p := range paths[:4] {
		if _, ok := n.bgScans[p]; ok {
			t.Errorf("%s: background-task scan kept", filepath.Base(p))
		}
	}
}

func cachedMetas(n *Node) int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return len(n.metas)
}
