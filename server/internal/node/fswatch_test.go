package node

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"
)

// events collects what a watcher emits.
type events struct {
	mu  sync.Mutex
	got []fsChanged
	ch  chan struct{}
}

func newEvents() *events { return &events{ch: make(chan struct{}, 64)} }

func (e *events) emit(c fsChanged) {
	e.mu.Lock()
	e.got = append(e.got, c)
	e.mu.Unlock()
	e.ch <- struct{}{}
}

func (e *events) next(t *testing.T) fsChanged {
	t.Helper()
	select {
	case <-e.ch:
	case <-time.After(5 * time.Second):
		t.Fatal("no fs.changed within 5s")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.got[len(e.got)-1]
}

func (e *events) none(t *testing.T, wait time.Duration) {
	t.Helper()
	select {
	case <-e.ch:
		t.Fatalf("unexpected fs.changed: %+v", e.got[len(e.got)-1])
	case <-time.After(wait):
	}
}

// fastWatch makes the watcher look every few milliseconds (before its first set).
func fastWatch(n *Node) {
	n.fsw.every, n.fsw.gitEvery = 20*time.Millisecond, 40*time.Millisecond
}

func waitExit(t *testing.T, w *fsWatcher) {
	t.Helper()
	w.mu.Lock()
	exited := w.exited
	w.mu.Unlock()
	if exited == nil {
		t.Fatal("no run to wait for")
	}
	select {
	case <-exited:
	case <-time.After(2 * time.Second):
		t.Fatal("the run's goroutine did not return")
	}
}

// writeWhole writes data to path whole: written beside it (in dir, which nobody watches), then renamed into place. A plain
// os.WriteFile creates the file empty and then writes it, and a poll between the two sees two real changes.
func writeWhole(t *testing.T, dir, path string, data []byte) {
	t.Helper()
	tmp := filepath.Join(dir, ".put-"+filepath.Base(path))
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}
}

func TestFsWatchReportsChangesInWatchedFoldersOnly(t *testing.T) {
	dir := t.TempDir()
	watched, other := filepath.Join(dir, "watched"), filepath.Join(dir, "other")
	for _, d := range []string{watched, other} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	n := &Node{home: dir}
	ev := newEvents()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fastWatch(n)
	n.fsw.start(ctx, ev.emit)
	defer n.fsw.close()

	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{{Path: watched, Cwd: dir}}})
	ev.none(t, 150*time.Millisecond) // the baseline is not a change

	if err := os.WriteFile(filepath.Join(other, "x.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	ev.none(t, 150*time.Millisecond) // a folder nobody has open is not looked at

	writeWhole(t, dir, filepath.Join(watched, "a.txt"), []byte("a"))
	if c := ev.next(t); len(c.Dirs) != 1 || c.Dirs[0] != watched || len(c.Git) != 0 {
		t.Fatalf("created file: %+v", c)
	}
	ev.none(t, 150*time.Millisecond) // reported once, not on every poll

	// A rewrite that changes the size shows in the listing, so it is a change too.
	writeWhole(t, dir, filepath.Join(watched, "a.txt"), []byte("longer"))
	if c := ev.next(t); len(c.Dirs) != 1 {
		t.Fatalf("rewritten file: %+v", c)
	}
	ev.none(t, 150*time.Millisecond)

	// Gone in one step (RemoveAll empties it first: a poll in between sees two real changes), then removed.
	if err := os.Rename(watched, filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(filepath.Join(dir, "gone")); err != nil {
		t.Fatal(err)
	}
	if c := ev.next(t); len(c.Dirs) != 1 {
		t.Fatalf("removed folder: %+v", c)
	}
	ev.none(t, 150*time.Millisecond) // staying away is not a change
}

// A folder that stays in the set keeps its signature across set calls, so re-sending the same set is silent.
func TestFsWatchSetKeepsWhatStays(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	ev := newEvents()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fastWatch(n)
	n.fsw.start(ctx, ev.emit)
	defer n.fsw.close()

	d := fsWatchDir{Path: dir, Cwd: dir}
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{d}})
	n.fsw.set(n, fsWatchParams{Dirs: []fsWatchDir{d}})
	ev.none(t, 150*time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	ev.next(t)
}

// The lifecycle: an empty set ends the goroutine, a new set starts one, the connection ending ends it, and the
// watcher never runs without a set.
func TestFsWatchLifecycleLeavesNothingRunning(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	before := runtime.NumGoroutine()

	ctx, cancel := context.WithCancel(context.Background())
	fastWatch(n)
	n.fsw.start(ctx, newEvents().emit)
	d := fsWatchParams{Dirs: []fsWatchDir{{Path: dir, Cwd: dir}}}

	n.fsw.set(n, d)
	waitRunning(t, &n.fsw, true)
	first := n.fsw.exited
	n.fsw.set(n, fsWatchParams{}) // the last panel closed
	waitExit(t, &n.fsw)
	waitRunning(t, &n.fsw, false)

	n.fsw.set(n, d) // opened again: a fresh run
	waitRunning(t, &n.fsw, true)
	if n.fsw.exited == first {
		t.Fatal("a second run reused the finished one")
	}
	cancel() // the hub connection ends with the panel still open
	waitExit(t, &n.fsw)
	waitRunning(t, &n.fsw, false)

	n.fsw.set(n, d) // a set after the connection ended starts nothing
	waitRunning(t, &n.fsw, false)
	n.fsw.close()

	// Only the run itself is allowed to come and go: nothing is left over once they are all over.
	deadline := time.Now().Add(2 * time.Second)
	for runtime.NumGoroutine() > before && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if got := runtime.NumGoroutine(); got > before {
		t.Fatalf("goroutines: %d before, %d after", before, got)
	}
}

func waitRunning(t *testing.T, w *fsWatcher, want bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		w.mu.Lock()
		got := w.running
		w.mu.Unlock()
		if got == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("running never became %v", want)
}

// Limits: a browser sending more than the cap is cut to it, not refused.
func TestFsWatchCapsTheSet(t *testing.T) {
	dir := t.TempDir()
	n := &Node{home: dir}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fastWatch(n)
	n.fsw.start(ctx, newEvents().emit)
	defer n.fsw.close()
	var p fsWatchParams
	for i := 0; i < maxFsWatchDirs+10; i++ {
		p.Dirs = append(p.Dirs, fsWatchDir{Path: filepath.Join(dir, string(rune('a'+i%26)), string(rune('a'+i/26))), Cwd: dir})
	}
	for i := 0; i < maxFsWatchGit+5; i++ {
		p.Git = append(p.Git, filepath.Join(dir, string(rune('a'+i))))
	}
	n.fsw.set(n, p)
	n.fsw.mu.Lock()
	defer n.fsw.mu.Unlock()
	if len(n.fsw.dirs) != maxFsWatchDirs || len(n.fsw.git) != maxFsWatchGit {
		t.Fatalf("watching %d folders and %d git dirs", len(n.fsw.dirs), len(n.fsw.git))
	}
}

// A folder over the cap is watched as the tree shows it (the first maxDirEntries in treeOrder), not as os.ReadDir lists
// it: an edit or rename of a shown entry that sorts past the cap in byte order is a change, and an edit of an entry the
// tree leaves out is not.
func TestFsWatchSignatureFollowsTheTreeOrderPastTheCap(t *testing.T) {
	n, home := testNode(t)
	dir := filepath.Join(home, "big")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxDirEntries {
		writeEmpty(t, dir, fmt.Sprintf("B%05d", i))
	}
	writeEmpty(t, dir, "a-first")
	if err := os.Mkdir(filepath.Join(dir, "zz-sub"), 0o700); err != nil {
		t.Fatal(err)
	}
	w := fsWatchDir{Path: dir}
	sig := n.dirSignature(w)
	changed := func(what string) {
		t.Helper()
		now := n.dirSignature(w)
		if now == sig {
			t.Fatalf("%s: signature unchanged", what)
		}
		sig = now
	}

	// Shown second (after zz-sub) but past the 2000th name in byte order.
	writeWhole(t, home, filepath.Join(dir, "a-first"), []byte("edited"))
	changed("edit of a-first")
	if err := os.Rename(filepath.Join(dir, "a-first"), filepath.Join(dir, "a-second")); err != nil {
		t.Fatal(err)
	}
	changed("rename of a-first")
	// A file added inside the subfolder changes its time, which the tree shows.
	later := time.Now().Add(time.Hour)
	if err := os.Chtimes(filepath.Join(dir, "zz-sub"), later, later); err != nil {
		t.Fatal(err)
	}
	changed("new time of zz-sub")

	// B01998 and B01999 are past the cap in the tree's order: the listing does not show them.
	writeWhole(t, home, filepath.Join(dir, fmt.Sprintf("B%05d", maxDirEntries-1)), []byte("hidden"))
	if now := n.dirSignature(w); now != sig {
		t.Fatal("an edit of an entry the tree does not show changed the signature")
	}
}

// A folder outside what the path guard allows is never read by the poll: its signature is the error's.
func TestFsWatchRefusesGuardedFolders(t *testing.T) {
	n := &Node{home: t.TempDir()}
	if got := n.dirSignature(fsWatchDir{Path: "/Users/x/Library/Mobile Documents/com~apple~CloudDocs/a"}); got != "err" {
		t.Fatalf("signature = %q", got)
	}
}

func hasGit(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
}

func gitIn(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=t", "-c", "user.email=t@example.com", "-c", "commit.gpgsign=false"}, args...)...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v\n%s", args, err, out)
	}
}

func TestFsWatchGitStatusChanges(t *testing.T) {
	hasGit(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitIn(t, dir, "add", ".")
	gitIn(t, dir, "commit", "-q", "-m", "init")

	n := &Node{home: dir}
	ev := newEvents()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	fastWatch(n)
	n.fsw.start(ctx, ev.emit)
	defer n.fsw.close()
	n.fsw.set(n, fsWatchParams{Git: []string{dir}})
	ev.none(t, 300*time.Millisecond)

	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("two\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := ev.next(t); len(c.Git) != 1 || c.Git[0] != dir || len(c.Dirs) != 0 {
		t.Fatalf("modified: %+v", c)
	}
	// Editing a file that is already modified leaves the status text alone; its time and size still move.
	time.Sleep(20 * time.Millisecond)
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("three lines\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if c := ev.next(t); len(c.Git) != 1 {
		t.Fatalf("modified again: %+v", c)
	}
	ev.none(t, 300*time.Millisecond)
}

func TestListDirMarksIgnored(t *testing.T) {
	hasGit(t)
	dir := t.TempDir()
	gitIn(t, dir, "init", "-q")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("build/\n*.log\n"), 0o600))
	must(os.Mkdir(filepath.Join(dir, "build"), 0o700))
	must(os.Mkdir(filepath.Join(dir, "src"), 0o700))
	must(os.WriteFile(filepath.Join(dir, "a.log"), nil, 0o600))
	must(os.WriteFile(filepath.Join(dir, "b.go"), nil, 0o600))

	n := &Node{home: dir}
	res, err := n.listDir(dir, "")
	must(err)
	ignored := map[string]bool{}
	for _, e := range res["entries"].([]DirEntry) {
		ignored[e.Name] = e.Ignored
	}
	want := map[string]bool{"build": true, "a.log": true, "src": false, "b.go": false, ".gitignore": false}
	for name, w := range want {
		if got, ok := ignored[name]; !ok || got != w {
			t.Errorf("%s: ignored=%v (listed %v), want %v", name, got, ok, w)
		}
	}

	// Outside a repository nothing is marked.
	plain := t.TempDir()
	must(os.WriteFile(filepath.Join(plain, "a.log"), nil, 0o600))
	res, err = (&Node{home: plain}).listDir(plain, "")
	must(err)
	for _, e := range res["entries"].([]DirEntry) {
		if e.Ignored {
			t.Errorf("%s marked ignored outside a repository", e.Name)
		}
	}
}

// A name git would read as pathspec magic (":!x", ":(glob)y") used to stop check-ignore with a fatal error at that
// name; it sorts before letters, so a.log and every other name after it lost its Ignored mark.
func TestListDirMarksIgnoredBesideMagicNames(t *testing.T) {
	hasGit(t)
	reviewGitEnv(t)
	list := func(odd []string) map[string]bool {
		t.Helper()
		dir := t.TempDir()
		gitIn(t, dir, "init", "-q")
		files := map[string]string{".gitignore": "*.log\n", "a.log": "", "keep.txt": ""}
		for _, name := range odd {
			files[name] = ""
		}
		for name, body := range files {
			if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		res, err := (&Node{home: dir}).listDir(dir, "")
		if err != nil {
			t.Fatal(err)
		}
		ignored := map[string]bool{}
		for _, e := range res["entries"].([]DirEntry) {
			if e.Name != ".git" {
				ignored[e.Name] = e.Ignored
			}
		}
		if len(ignored) != len(files) {
			t.Fatalf("listed %v, want the %d files written", ignored, len(files))
		}
		return ignored
	}
	want := map[string]bool{".gitignore": false, "a.log": true, "keep.txt": false}
	check := func(got map[string]bool, odd []string) {
		t.Helper()
		for name, w := range want {
			if got[name] != w {
				t.Errorf("with %q: %s ignored=%v, want %v", odd, name, got[name], w)
			}
		}
		for _, name := range odd {
			if got[name] {
				t.Errorf("with %q: %s marked ignored", odd, name)
			}
		}
	}
	check(list(nil), nil)
	if runtime.GOOS == "windows" { // ':' cannot be part of a file name there
		return
	}
	odd := []string{":!odd.txt", ":(glob)y", ":^x"}
	check(list(odd), odd)
}

// The RPC the hub calls: the JSON of fs.watch becomes the watched set, and an empty one releases it.
func TestFsWatchRPC(t *testing.T) {
	n, dir := testNode(t)
	ev := newEvents()
	fastWatch(n)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	n.fsw.start(ctx, ev.emit)
	defer n.fsw.close()

	params, _ := json.Marshal(map[string]any{"dirs": []map[string]string{{"path": dir, "cwd": dir}}, "git": []string{}})
	if _, err := n.handle(ctx, "fs.watch", params); err != nil {
		t.Fatal(err)
	}
	waitRunning(t, &n.fsw, true)
	if err := os.WriteFile(filepath.Join(dir, "a"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if c := ev.next(t); len(c.Dirs) != 1 || c.Dirs[0] != dir {
		t.Fatalf("event = %+v", c)
	}
	if _, err := n.handle(ctx, "fs.watch", json.RawMessage(`{"dirs":[],"git":[]}`)); err != nil {
		t.Fatal(err)
	}
	waitExit(t, &n.fsw)
	waitRunning(t, &n.fsw, false)
	if _, err := n.handle(ctx, "fs.watch", json.RawMessage(`not json`)); err == nil {
		t.Fatal("a malformed call was accepted")
	}
}
