package node

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/proto"
)

func (n *Node) dirs(path string) (*proto.DirList, error) {
	if path == "" || path == "~" {
		path = n.home
	} else if strings.HasPrefix(path, "~/") {
		path = filepath.Join(n.home, path[2:])
	}
	path = filepath.Clean(path)
	ents, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	res := &proto.DirList{Path: path, Parent: filepath.Dir(path), Entries: []proto.DirEntry{}, Recent: n.recentDirs()}
	if path == "/" {
		res.Parent = ""
	}
	for _, e := range ents {
		name := e.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		full := filepath.Join(path, name)
		if !e.IsDir() {
			if e.Type()&os.ModeSymlink == 0 {
				continue
			}
			if st, err := os.Stat(full); err != nil || !st.IsDir() {
				continue
			}
		}
		if strings.Contains(full, "Mobile Documents") { // never touch iCloud Drive: it hangs on locked Macs
			continue
		}
		_, gerr := os.Stat(filepath.Join(full, ".git"))
		res.Entries = append(res.Entries, proto.DirEntry{Name: name, Path: full, IsGit: gerr == nil})
	}
	sort.Slice(res.Entries, func(i, j int) bool {
		return strings.ToLower(res.Entries[i].Name) < strings.ToLower(res.Entries[j].Name)
	})
	return res, nil
}

// dirsByUse is every working directory a session on this node is known to have run in, with when it was last used
// (its newest transcript, deleted ones included); a running agent's without a transcript yet has the zero time.
// Nothing is read from disk.
func (n *Node) dirsByUse() map[string]time.Time {
	n.mu.Lock()
	defer n.mu.Unlock()
	last := map[string]time.Time{}
	for _, m := range n.metas {
		if m.cwd != "" && m.mtime.After(last[m.cwd]) {
			last[m.cwd] = m.mtime
		}
	}
	for dir, at := range n.goneDirs { // transcripts pruned since they were deleted (meta_prune.go)
		if at.After(last[dir]) {
			last[dir] = at
		}
	}
	for _, a := range n.agents {
		if _, ok := last[a.Cwd]; a.Cwd != "" && !ok {
			last[a.Cwd] = time.Time{} // running but no transcript yet: listed, oldest
		}
	}
	return last
}

func (n *Node) recentDirs() []proto.RecentDir {
	last := n.dirsByUse()
	var out []proto.RecentDir
	for p, t := range last {
		if identity.IsAssistantDir(p) {
			continue
		}
		if st, err := os.Stat(p); err != nil || !st.IsDir() {
			continue
		}
		out = append(out, proto.RecentDir{Path: p, Name: filepath.Base(p), LastUsed: t.UTC()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].LastUsed.After(out[j].LastUsed) })
	if len(out) > 15 {
		out = out[:15]
	}
	return out
}
