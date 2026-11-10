package node

import (
	"errors"
	"io/fs"
	"os"
	"sync"
	"time"
)

// metaPruneEvery: how often scanOffline drops what the node keeps for transcripts that are gone (pruneGoneMetas).
const metaPruneEvery = time.Minute

var metaPruneAt struct {
	sync.Mutex
	at  time.Time
	key string // whose prune it is: another node's (tests run many in one process) prunes its own, as offlineCache
}

// maybePruneGoneMetas runs pruneGoneMetas at most every metaPruneEvery. files are the transcripts scanOffline globbed:
// they exist, so only the paths it did not glob (live sessions' and others read on their own) are stat'ed.
func (n *Node) maybePruneGoneMetas(files []string) {
	key := n.offlineCacheKey()
	metaPruneAt.Lock()
	due := key != metaPruneAt.key || time.Since(metaPruneAt.at) >= metaPruneEvery
	if due {
		metaPruneAt.at, metaPruneAt.key = time.Now(), key
	}
	metaPruneAt.Unlock()
	if !due {
		return
	}
	present := make(map[string]bool, len(files))
	for _, p := range files {
		present[p] = true
	}
	n.pruneGoneMetas(present)
}

// metaStamp tells whether a quick meta was written again: every read sets readAt, a rewind clears it with mtime.
// scan is set instead for a background-task scan with no quick meta (pruneCandidates).
type metaStamp struct {
	size          int64
	mtime, readAt time.Time
	scan          *bgScan
}

func stampOf(m quickMeta) metaStamp { return metaStamp{size: m.size, mtime: m.mtime, readAt: m.readAt} }

func (s metaStamp) same(o metaStamp) bool {
	return s.size == o.size && s.mtime.Equal(o.mtime) && s.readAt.Equal(o.readAt)
}

// pruneGoneMetas drops what n.metas and n.bgScans hold for transcripts that no longer exist: a long-running node kept a
// full quick meta and background-task scan (title, last words, background tasks, workflows: several KB, more for long
// turns) for every transcript it had ever read. present are paths known to exist, which are not stat'ed. dirsByUse
// lists the directories of every transcript the node has seen (recent dirs, workDirs): a gone one's cwd and mtime go
// into n.goneDirs, one entry per directory, so that list stays as it was.
func (n *Node) pruneGoneMetas(present map[string]bool) {
	check := n.pruneCandidates(present)
	var gone []string
	for p := range check { // off n.mu: a slow disk must not stall every session list
		if _, err := os.Stat(p); errors.Is(err, fs.ErrNotExist) {
			gone = append(gone, p)
		}
	}
	n.dropGoneMetas(check, gone)
}

// pruneCandidates are the metas not in present, with how each looked, and the background-task scans not in present
// that have no meta: a resident node's new connection starts with the last one's scans (carry.go) and no metas, so a
// transcript deleted in between would keep its scan for good.
func (n *Node) pruneCandidates(present map[string]bool) map[string]metaStamp {
	n.mu.Lock()
	defer n.mu.Unlock()
	check := map[string]metaStamp{}
	for p, m := range n.metas {
		if !present[p] {
			check[p] = stampOf(m)
		}
	}
	for p, sc := range n.bgScans {
		if _, ok := n.metas[p]; !ok && !present[p] {
			check[p] = metaStamp{scan: sc}
		}
	}
	return check
}

// dropGoneMetas drops gone paths' metas and scans, leaving any read again since check was taken. A dropped meta's cwd
// and mtime are kept in n.goneDirs for dirsByUse.
func (n *Node) dropGoneMetas(check map[string]metaStamp, gone []string) {
	if len(gone) == 0 {
		return
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, p := range gone {
		m, ok := n.metas[p]
		if sc := check[p].scan; !ok && sc != nil && n.bgScans[p] == sc {
			delete(n.bgScans, p) // a scan with no meta, still the one checked (by pointer: n.mu holds no bgScan's lock)
			continue
		}
		if !ok || !stampOf(m).same(check[p]) {
			continue // read again since the stat: the file may be back
		}
		if m.cwd != "" && m.mtime.After(n.goneDirs[m.cwd]) {
			if n.goneDirs == nil {
				n.goneDirs = map[string]time.Time{}
			}
			n.goneDirs[m.cwd] = m.mtime
		}
		delete(n.metas, p)
		delete(n.bgScans, p)
	}
}

// forgetBgScan drops path's background-task scan when its file is gone, if it is still sc (a caller may have made a new
// one since). A file made again at the path is then scanned from the start, not from the old one's offset. The caller
// may hold sc.mu: n.mu is never held while taking a bgScan's lock.
func (n *Node) forgetBgScan(path string, sc *bgScan) {
	n.mu.Lock()
	if n.bgScans[path] == sc {
		delete(n.bgScans, path)
	}
	n.mu.Unlock()
}
