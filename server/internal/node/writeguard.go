package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"os"
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Where a client may change files (uploads into a folder, and later renames, moves, deletes; docs/protocol/protocol.md
// "Changing files"): inside the user's home only, once the read guard (pathguard.go) has followed every link, and never in
// the folders that run Persona and the Mac itself. Outside the home everything stays read-only.

// noWriteDirs are folders under the home that are never written to, whole: the hub's and the node's own state, and
// the Mac's Library (preferences, keychains, app data) that nothing here should edit by hand.
var noWriteDirs = []string{".persona", compat.StateDir, "Library"}

func errManagerReadOnly(path string) error {
	return &rpc.Error{Code: proto.CodeReadOnly, Message: fmt.Sprintf("%s is in a manager's folder, which is read-only from the phone", path)}
}

func errReadOnly(path string) error {
	return &rpc.Error{Code: proto.CodeReadOnly, Message: fmt.Sprintf("%s is read-only from the phone (only folders in the home are writable)", path)}
}

// writable says whether real (absolute, clean, links already followed) is a place a client may change.
func (n *Node) writable(real string) error {
	if err := n.offLimitsResolved(real); err != nil {
		return err
	}
	// A manager's own folder wherever its --dir put it (#5877), whole, as the data folder: its extensions' programs,
	// its knowledge base and its configuration are the manager's to change. With --dir the home itself, the home stays
	// writable (as with PERSONA_HOME=~). Only real folders are looked into: real's last component may be a name to
	// write (an upload's) or a link, which is not followed.
	start := filepath.Dir(real)
	if st, err := os.Lstat(real); err == nil && st.IsDir() {
		start = real
	}
	for d := start; d != filepath.Dir(d); d = filepath.Dir(d) {
		if isManagerDir(d) && !isHome(fold(d), n.home) {
			return errManagerReadOnly(real)
		}
	}
	// Nor is a manager.json written by a client: one naming a hub and a token would make a folder a manager's, its
	// credentials refused and the folder read-only, with nothing the client could undo.
	if strings.HasPrefix(fold(filepath.Base(real)), "manager.json") {
		return errReadOnly(real)
	}
	if n.home == "" {
		return errReadOnly(real)
	}
	p := fold(real)
	rel, ok := homeRel(p, n.home)
	if !ok {
		if isHome(p, n.home) {
			return nil
		}
		return errReadOnly(real)
	}
	for _, d := range noWriteDirs {
		if d = strings.ToLower(d); rel == d || strings.HasPrefix(rel, d+"/") {
			return errReadOnly(real)
		}
	}
	// The data folder wherever PERSONA_HOME puts it (its extensions, knowledge base, managers), as the read guard
	// finds it (#5694). With PERSONA_HOME=~ it is the home itself, which stays writable.
	for _, dir := range n.dataDirs() {
		if !isHome(dir, n.home) && (p == dir || strings.HasPrefix(p, dir+"/")) {
			return errReadOnly(real)
		}
	}
	return nil
}

// isHome: the folded path p is the home itself, as written or with its links followed.
func isHome(p, home string) bool {
	home = filepath.Clean(home)
	if p == fold(home) {
		return true
	}
	real, err := filepath.EvalSymlinks(home)
	return err == nil && p == fold(real)
}

// writableDir resolves a folder to write into like a listing does (~/, relative to cwd) and checks it is an
// existing directory a client may change. shown is the path as the app knows it, real the one behind its links.
func (n *Node) writableDir(path, cwd string) (shown, real string, err error) {
	if strings.TrimSpace(path) == "" {
		return "", "", errors.New("empty path")
	}
	shown, real, err = n.resolveDir(path, cwd)
	if err != nil {
		return "", "", err
	}
	if err := n.writable(real); err != nil {
		return "", "", err
	}
	st, err := os.Stat(real)
	if err != nil {
		return "", "", explainDenied(shown, n.home, err)
	}
	if !st.IsDir() {
		return "", "", fmt.Errorf("%s is not a folder", shown)
	}
	return shown, real, nil
}

// errExists: a file of that name is already in the folder; the app asks to replace it or keep both.
func errExists(name string) error {
	data, _ := json.Marshal(map[string]any{"name": name})
	return &rpc.Error{Code: proto.CodeExists, Message: fmt.Sprintf("%s already exists", name), Data: data}
}

// renameIfFree is renameExcl where the system has no such rename. A file is first linked at the new name, which fails
// if the name is taken, and then removed at the old one, so a file appearing meanwhile is never replaced. Where a link
// cannot be made (another disk, a file system without links), the old name cannot be removed, and for folders, links
// and the like, it is a check just before the rename: racy, a file appearing in between would be replaced.
func renameIfFree(from, to string) error {
	exists := &os.LinkError{Op: "rename", Old: from, New: to, Err: os.ErrExist}
	if _, err := os.Lstat(to); err == nil {
		return exists
	}
	if renameFreeCheckedHook != nil {
		renameFreeCheckedHook()
	}
	if st, err := os.Lstat(from); err == nil && st.Mode().IsRegular() {
		err := os.Link(from, to)
		if errors.Is(err, os.ErrExist) {
			return exists
		}
		if err == nil {
			if os.Remove(from) == nil {
				return nil
			}
			// The old name cannot be removed (a read-only file on Windows, which a rename still moves): take the
			// link back, so the file is not left in both places, and rename it as before.
			if err := os.Remove(to); err != nil {
				return err
			}
		}
	}
	return os.Rename(from, to)
}

// renameFreeCheckedHook, when set (by tests), runs in renameIfFree between the check and the move.
var renameFreeCheckedHook func()
