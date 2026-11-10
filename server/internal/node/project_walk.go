package node

import (
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// projectTranscripts lists what globProjects("*", "*.jsonl") and then globProjects("*", "*", "subagents", "*.jsonl")
// list, in the same order: every projects directory's transcripts, then every one's subagent transcripts. The usage
// meter asked for both every 10 seconds; the two globs read each projects directory twice, and the second one stat'ed
// every transcript to learn it is not a directory. One walk reads each directory once and takes that from the
// directory entry (walkProjects).
func (n *Node) projectTranscripts() []string {
	var mains, subs []string
	for _, p := range n.projectDirs() {
		if n.sessionScope != nil && !sessionScopeWithin(n.sessionScope.claude, p) {
			continue
		}
		var m, s []string
		if hasGlobMeta(p) { // the globs would read p as a pattern: let them
			m, _ = filepath.Glob(filepath.Join(p, "*", "*.jsonl"))
			s, _ = filepath.Glob(filepath.Join(p, "*", "*", "subagents", "*.jsonl"))
		} else {
			m, s = walkProjects(filepath.Clean(p))
		}
		for _, path := range m {
			if n.scopedTranscript(path) {
				mains = append(mains, path)
			}
		}
		for _, path := range s {
			if n.scopedTranscript(path) {
				subs = append(subs, path)
			}
		}
	}
	return append(mains, subs...)
}

// walkProjects returns what filepath.Glob returns for root/*/*.jsonl (mains) and root/*/*/subagents/*.jsonl (subs),
// root holding no glob metacharacters: the same names in the same order (sorted per directory, as Glob reads them),
// any kind of file matching the last part. Glob stats each candidate directory; here a directory entry that is a
// regular file is passed over without one, a directory is read at once, and only anything else (a symlink, or a kind
// the system does not report) is stat'ed first, so a link reaches what Glob reaches through it.
func walkProjects(root string) (mains, subs []string) {
	projects, _ := os.ReadDir(root)
	for _, project := range projects {
		dir := filepath.Join(root, project.Name())
		for _, e := range readDirFollowing(dir, project.Type()) {
			path := filepath.Join(dir, e.Name())
			if strings.HasSuffix(e.Name(), ".jsonl") {
				mains = append(mains, path)
			}
			for _, sub := range readDirFollowing(path, e.Type()) {
				if sub.Name() != "subagents" {
					continue
				}
				agents := filepath.Join(path, sub.Name())
				for _, a := range readDirFollowing(agents, sub.Type()) {
					if strings.HasSuffix(a.Name(), ".jsonl") {
						subs = append(subs, filepath.Join(agents, a.Name()))
					}
				}
			}
		}
	}
	return mains, subs
}

// readDirFollowing reads path, a directory entry of type typ, if it is a directory or leads to one; Glob reads the same
// ones (os.Stat, then the directory). A regular file needs no stat to be passed over.
func readDirFollowing(path string, typ fs.FileMode) []os.DirEntry {
	switch {
	case typ.IsRegular():
		return nil
	case typ != fs.ModeDir:
		if st, err := os.Stat(path); err != nil || !st.IsDir() {
			return nil
		}
	}
	entries, _ := os.ReadDir(path) // what it read before an error, as Glob keeps the names it read
	return entries
}

// hasGlobMeta reports whether path holds a character filepath.Glob treats as a pattern (its hasMeta).
func hasGlobMeta(path string) bool {
	meta := `*?[\`
	if runtime.GOOS == "windows" {
		meta = `*?[`
	}
	return strings.ContainsAny(path, meta)
}
