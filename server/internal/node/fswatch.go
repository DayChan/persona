package node

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// The web panels' live Files / Git (docs/web/web.md "Files and Git auto-refresh"): the hub tells the node which folders the
// browsers have open (fs.watch, the whole set each time), the node looks at them every fsWatchEvery and pushes
// fs.changed {dirs, git} when what a listing shows (names, sizes, times) or what `git status` shows changed.
//
// Polling, not inotify / fsnotify: the module is not in go.mod, a recursive watch does not exist on Linux, and a
// few folders every couple of seconds cost less than the git processes the Git half runs anyway. The set is exactly
// the open folders, replaced on every call and emptied when the last panel closes, so nothing is watched (and no
// goroutine runs) for a panel that is gone; a node serves one hub connection, so the connection ending stops it too.

const (
	maxFsWatchDirs = 32
	maxFsWatchGit  = 8
	// maxGitStat is how many changed files' times join the Git signature (an edit to a file that was already
	// modified leaves `git status` as it was).
	maxGitStat = 300
)

// How often the folders and the Git directories are looked at (a watcher's own copies are what tests shorten).
const (
	fsWatchEvery  = 2 * time.Second
	gitWatchEvery = 5 * time.Second
)

// fsWatchDir is one folder to watch, as the browser names it (path and the session's cwd it is relative to).
type fsWatchDir struct {
	Path string `json:"path"`
	Cwd  string `json:"cwd"`
}

type fsWatchParams struct {
	Dirs []fsWatchDir `json:"dirs"`
	// Git lists the working directories whose `git status` to watch.
	Git []string `json:"git"`
}

// fsChanged is the fs.changed event: the folders (as named in fs.watch) and Git directories that changed.
type fsChanged struct {
	Dirs []string `json:"dirs,omitempty"`
	Git  []string `json:"git,omitempty"`
}

type gitSig struct {
	sig     string
	checked time.Time
}

// fsWatcher is a node's set of watched folders and the one goroutine looking at them while the set is not empty.
type fsWatcher struct {
	mu   sync.Mutex
	ctx  context.Context // the hub connection's: ends every run
	emit func(fsChanged)

	dirs    map[fsWatchDir]string // folder → the signature last seen
	git     map[string]gitSig     // cwd → the signature last seen
	stop    chan struct{}         // closes to end the run in progress
	exited  chan struct{}         // closes when that run's goroutine has returned
	running bool

	every, gitEvery time.Duration
}

// start binds the watcher to a connection: ctx ending ends any run, emit sends the events.
func (w *fsWatcher) start(ctx context.Context, emit func(fsChanged)) {
	w.mu.Lock()
	w.ctx, w.emit = ctx, emit
	if w.every == 0 {
		w.every, w.gitEvery = fsWatchEvery, gitWatchEvery
	}
	w.mu.Unlock()
}

// set replaces the watched set. Folders that stay keep their signature (no event for them); new ones are looked at
// now, so only changes after this call are reported. An empty set ends the run.
func (w *fsWatcher) set(n *Node, p fsWatchParams) {
	dirs := p.Dirs
	if len(dirs) > maxFsWatchDirs {
		dirs = dirs[:maxFsWatchDirs]
	}
	gits := p.Git
	if len(gits) > maxFsWatchGit {
		gits = gits[:maxFsWatchGit]
	}
	nd := make(map[fsWatchDir]string, len(dirs))
	ng := make(map[string]gitSig, len(gits))
	w.mu.Lock()
	for _, d := range dirs {
		nd[d] = w.dirs[d]
	}
	for _, g := range gits {
		ng[g] = w.git[g]
	}
	w.mu.Unlock()
	// Baselines of what is new, outside the lock (they read the disk and run git).
	for d, sig := range nd {
		if sig == "" {
			nd[d] = n.dirSignature(d)
		}
	}
	for g, s := range ng {
		if s.sig == "" {
			ng[g] = gitSig{sig: n.gitSignature(w.runCtx(), g), checked: time.Now()}
		}
	}

	w.mu.Lock()
	defer w.mu.Unlock()
	w.dirs, w.git = nd, ng
	if len(nd) == 0 && len(ng) == 0 {
		w.endLocked()
		return
	}
	if !w.running && w.ctx != nil && w.ctx.Err() == nil {
		w.running = true
		w.stop, w.exited = make(chan struct{}), make(chan struct{})
		go w.run(n, w.ctx, w.stop, w.exited, w.every, w.gitEvery)
	}
}

func (w *fsWatcher) runCtx() context.Context {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.ctx == nil {
		return context.Background()
	}
	return w.ctx
}

// endLocked ends the run in progress, if any. w.mu is held.
func (w *fsWatcher) endLocked() {
	if w.running {
		close(w.stop)
		w.running = false
	}
}

// close drops the set and ends the run (the hub connection is over).
func (w *fsWatcher) close() {
	w.mu.Lock()
	w.dirs, w.git = nil, nil
	w.endLocked()
	w.mu.Unlock()
}

func (w *fsWatcher) run(n *Node, ctx context.Context, stop <-chan struct{}, exited chan<- struct{}, every, gitEvery time.Duration) {
	defer close(exited)
	tick := time.NewTicker(every)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			w.mu.Lock()
			w.endLocked()
			w.mu.Unlock()
			return
		case <-stop:
			return
		case <-tick.C:
		}
		w.poll(n, ctx, stop, every, gitEvery)
	}
}

// poll looks at every watched folder (and, when due, every Git directory) once and emits what changed.
func (w *fsWatcher) poll(n *Node, ctx context.Context, stop <-chan struct{}, every, gitEvery time.Duration) {
	w.mu.Lock()
	dirs := make([]fsWatchDir, 0, len(w.dirs))
	for d := range w.dirs {
		dirs = append(dirs, d)
	}
	var gits []string
	for g, s := range w.git {
		if time.Since(s.checked) >= gitEvery-every/2 {
			gits = append(gits, g)
		}
	}
	w.mu.Unlock()

	var ch fsChanged
	dsig := make(map[fsWatchDir]string, len(dirs))
	for _, d := range dirs {
		dsig[d] = n.dirSignature(d)
	}
	gsig := make(map[string]string, len(gits))
	for _, g := range gits {
		select {
		case <-stop:
			return
		default:
		}
		gsig[g] = n.gitSignature(ctx, g)
	}

	w.mu.Lock()
	for d, sig := range dsig {
		if old, ok := w.dirs[d]; ok && old != sig {
			w.dirs[d] = sig
			ch.Dirs = append(ch.Dirs, d.Path)
		}
	}
	for g, sig := range gsig {
		if old, ok := w.git[g]; ok {
			if old.sig != sig {
				ch.Git = append(ch.Git, g)
			}
			w.git[g] = gitSig{sig: sig, checked: time.Now()}
		}
	}
	emit := w.emit
	w.mu.Unlock()

	if (len(ch.Dirs) > 0 || len(ch.Git) > 0) && emit != nil && ctx.Err() == nil {
		sort.Strings(ch.Dirs)
		sort.Strings(ch.Git)
		emit(ch)
	}
}

// dirSignature is a hash of what a listing of d shows: the entry count, and the name, type, size and time of the entries
// listDir keeps. A folder within maxDirEntries shows all of them; a bigger one shows the first maxDirEntries in treeOrder
// (not os.ReadDir's byte order), so only such a folder pays for the sort and for following its links, and still only
// the shown entries' Info is read.
// A folder that cannot be read has the signature of its error (so it going away is a change, and staying away is not).
func (n *Node) dirSignature(d fsWatchDir) string {
	_, real, err := n.resolveDir(d.Path, d.Cwd)
	if err != nil {
		return "err"
	}
	ents, err := os.ReadDir(real)
	if err != nil {
		return "err"
	}
	shown := ents
	if len(ents) > maxDirEntries {
		shown = make([]os.DirEntry, maxDirEntries)
		for i, t := range n.treeOrder(real, ents)[:maxDirEntries] {
			shown[i] = t.e
		}
	}
	h := fnv.New64a()
	for _, e := range shown {
		fmt.Fprintf(h, "%s\x00%d\x00", e.Name(), e.Type())
		if info, err := e.Info(); err == nil {
			fmt.Fprintf(h, "%d\x00%d\x00", info.Size(), info.ModTime().UnixNano())
		}
	}
	return fmt.Sprintf("%d:%x", len(ents), h.Sum64())
}

// gitSignature is a hash of `git status` for cwd plus the times of the files it lists. Not a repository, or git
// failing, is a signature of its own ("none" / "err") rather than an empty one, which would read as "not looked at".
func (n *Node) gitSignature(ctx context.Context, cwd string) string {
	dir, err := n.gitDir(cwd)
	if err != nil {
		return "err"
	}
	root, err := gitTimed(ctx, dir, 5*time.Second, "rev-parse", "--show-toplevel")
	if err != nil {
		return "none"
	}
	root = strings.TrimSpace(root)
	out, err := gitTimed(ctx, root, 8*time.Second, "status", "--porcelain=v2", "--branch", "-z", "--untracked-files=all")
	if err != nil {
		return "err"
	}
	h := fnv.New64a()
	h.Write([]byte(out))
	var st GitStatus
	for i, f := range parseStatusV2(out, &st) {
		if i >= maxGitStat {
			break
		}
		if info, err := os.Lstat(filepath.Join(root, f.Path)); err == nil {
			fmt.Fprintf(h, "%s\x00%d\x00%d\x00", f.Path, info.Size(), info.ModTime().UnixNano())
		}
	}
	return fmt.Sprintf("%x", h.Sum64())
}

// fsWatch is the fs.watch call: replace the watched set.
func (n *Node) fsWatch(params json.RawMessage) (any, error) {
	var p fsWatchParams
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, err
	}
	n.fsw.set(n, p)
	return map[string]any{"ok": true}, nil
}
