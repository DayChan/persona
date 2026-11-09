package node

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Renaming, moving and deleting a machine's files from the Files browser (docs/protocol/protocol.md "Changing files", #315):
// only what the write guard allows (writeguard.go), never replacing anything unasked (renameExcl), and a delete is
// a move to the system's trash that file.restore undoes. The node forgets nothing it would need for that: a trash_id
// carries where the file went and where it came from, so a restarted node or hub still puts it back.

// editEntry is a file or folder to change. The folder it is in is resolved like a listing (links followed); the
// name itself is not, so a link is renamed, moved or trashed as the link.
type editEntry struct {
	shown string // the path as the app gave it, for the answers
	real  string // its folder's real path + its name
	info  os.FileInfo
}

func errNotFound(path string) error {
	return &rpc.Error{Code: proto.CodeNotFound, Message: fmt.Sprintf("%s is not there", path)}
}

func (n *Node) entry(path, cwd string) (editEntry, error) {
	shown := strings.TrimSpace(path)
	if s := strings.TrimRight(shown, "/"); s != "" {
		shown = s
	}
	abs := n.expandPath(shown, cwd)
	if abs == "" {
		return editEntry{}, errors.New("empty path")
	}
	abs = absPath(abs)
	parent, err := n.realPath(filepath.Dir(abs))
	if err != nil {
		return editEntry{}, explainDenied(shown, n.home, err)
	}
	real := filepath.Join(parent, filepath.Base(abs))
	if real == parent || isHome(fold(real), n.home) { // "/" or the home itself
		return editEntry{}, errReadOnly(shown)
	}
	if err := n.writable(real); err != nil {
		return editEntry{}, err
	}
	info, err := os.Lstat(real)
	if errors.Is(err, os.ErrNotExist) {
		return editEntry{}, errNotFound(shown)
	}
	if err != nil {
		return editEntry{}, explainDenied(shown, n.home, err)
	}
	return editEntry{shown: shown, real: real, info: info}, nil
}

// checkName refuses what cannot be one file's name in a folder.
func checkName(name string) error {
	if strings.TrimSpace(name) == "" || name == "." || name == ".." || strings.ContainsAny(name, "/\x00") {
		return fmt.Errorf("%q can't be a file's name", name)
	}
	return nil
}

// inFolder is the path of name beside shown, as the app writes paths ("~/a/b" + "c" = "~/a/c").
func inFolder(dir, name string) string {
	switch {
	case dir == "":
		return name
	case strings.HasSuffix(dir, "/"):
		return dir + name
	}
	return dir + "/" + name
}

func folderOf(shown string) string {
	i := strings.LastIndex(shown, "/")
	switch {
	case i < 0:
		return ""
	case i == 0:
		return "/"
	}
	return shown[:i]
}

// explainRename words the errors a rename across folders gives.
func explainRename(shown string, err error) error {
	if errors.Is(err, syscall.EXDEV) {
		return fmt.Errorf("%s is on another disk; moving between disks isn't supported", shown)
	}
	return err
}

func (n *Node) renameFile(path, cwd, name string) (map[string]any, error) {
	if err := checkName(name); err != nil {
		return nil, err
	}
	e, err := n.entry(path, cwd)
	if err != nil {
		return nil, err
	}
	shown := inFolder(folderOf(e.shown), name)
	to := filepath.Join(filepath.Dir(e.real), name)
	if to == e.real {
		return map[string]any{"path": shown}, nil
	}
	if err := n.writable(to); err != nil {
		return nil, err
	}
	if st, err := os.Lstat(to); err == nil && os.SameFile(st, e.info) {
		// Only the case changes, on a disk that ignores case: the same file under the other name.
		if err := os.Rename(e.real, to); err != nil {
			return nil, err
		}
		return map[string]any{"path": shown}, nil
	}
	if err := renameExcl(e.real, to); err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, errExists(name)
		}
		return nil, err
	}
	n.logger.Printf("renamed from the phone: %s", to)
	return map[string]any{"path": shown}, nil
}

// moveFiles moves paths into dir. Without a conflict policy a taken name refuses the whole move before anything
// moves; keep puts it beside as "name 2.ext"; replace moves the one that was there to the trash first (nothing is
// deleted for good) and never replaces a folder.
func (n *Node) moveFiles(paths []string, cwd, dir, conflict string) (map[string]any, error) {
	switch conflict {
	case "", proto.UploadConflictReplace, proto.UploadConflictKeep:
	default:
		return nil, fmt.Errorf("unknown conflict %q", conflict)
	}
	if len(paths) == 0 {
		return nil, errors.New("no paths")
	}
	_, realDir, err := n.writableDir(dir, cwd)
	if err != nil {
		return nil, err
	}
	shownDir := strings.TrimSpace(dir)
	if s := strings.TrimRight(shownDir, "/"); s != "" {
		shownDir = s
	}
	entries := make([]editEntry, 0, len(paths))
	names := map[string]bool{}
	for _, p := range paths {
		e, err := n.entry(p, cwd)
		if err != nil {
			return nil, err
		}
		if e.info.IsDir() && (realDir == e.real || strings.HasPrefix(realDir, e.real+"/")) {
			return nil, fmt.Errorf("%s can't be moved into itself", e.shown)
		}
		name := filepath.Base(e.real)
		if filepath.Dir(e.real) != realDir {
			if names[fold(name)] && conflict != proto.UploadConflictKeep {
				return nil, errExists(name)
			}
			names[fold(name)] = true
			if _, err := os.Lstat(filepath.Join(realDir, name)); err == nil && conflict == "" {
				return nil, errExists(name)
			}
		}
		entries = append(entries, e)
	}
	moved := make([]map[string]string, 0, len(entries))
	for _, e := range entries {
		name := filepath.Base(e.real)
		if filepath.Dir(e.real) == realDir { // already there
			moved = append(moved, map[string]string{"from": e.shown, "to": inFolder(shownDir, name)})
			continue
		}
		final, err := n.moveOne(e, realDir, name, conflict)
		if err != nil {
			return nil, explainRename(e.shown, err)
		}
		moved = append(moved, map[string]string{"from": e.shown, "to": inFolder(shownDir, final)})
	}
	n.logger.Printf("moved from the phone: %d into %s", len(moved), realDir)
	return map[string]any{"moved": moved}, nil
}

// moveOne moves e into realDir as name, or what the conflict policy makes of a taken name; it returns the name used.
func (n *Node) moveOne(e editEntry, realDir, name, conflict string) (string, error) {
	to := filepath.Join(realDir, name)
	if err := n.writable(to); err != nil {
		return "", err
	}
	var replaced string // the trash id of the destination a replace moved aside
	if conflict == proto.UploadConflictReplace {
		if st, err := os.Lstat(to); err == nil {
			if st.IsDir() || e.info.IsDir() {
				return "", errExists(name)
			}
			if replaced, err = n.toTrash(to, to); err != nil {
				return "", err
			}
		}
	}
	err := renameExcl(e.real, to)
	if err != nil && replaced != "" {
		// Nothing took the destination's place (the source went away, another file system): it goes back (#1968).
		if _, rerr := n.restoreFiles([]string{replaced}); rerr != nil {
			return "", fmt.Errorf("%w; %s is in the trash and could not be put back: %v", err, name, rerr)
		}
	}
	if conflict != proto.UploadConflictKeep || !errors.Is(err, os.ErrExist) {
		if errors.Is(err, os.ErrExist) {
			return "", errExists(name)
		}
		return name, err
	}
	stem, ext := splitName(name)
	if e.info.IsDir() {
		stem, ext = name, ""
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s %d%s", stem, i, ext)
		if err := renameExcl(e.real, filepath.Join(realDir, candidate)); !errors.Is(err, os.ErrExist) {
			return candidate, err
		}
	}
	return "", errExists(name)
}

// trashID is what file.restore needs to put a trashed file back: its name in the trash, the real path it had and
// the path as the app knew it. Restoring checks the place again like any write, so a made-up id can do nothing a
// move could not.
type trashID struct {
	Name  string `json:"n"`
	Real  string `json:"r"`
	Shown string `json:"s"`
}

func (t trashID) encode() string {
	b, _ := json.Marshal(t)
	return base64.RawURLEncoding.EncodeToString(b)
}

func decodeTrashID(s string) (trashID, error) {
	var t trashID
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err == nil {
		err = json.Unmarshal(b, &t)
	}
	if err != nil || checkName(t.Name) != nil || !filepath.IsAbs(t.Real) || filepath.Clean(t.Real) != t.Real {
		return trashID{}, fmt.Errorf("bad trash_id")
	}
	return t, nil
}

// trashDirs is where this system's trash keeps files, and (freedesktop) the .trashinfo of each: macOS ~/.Trash,
// Linux $XDG_DATA_HOME/Trash or ~/.local/share/Trash.
func (n *Node) trashDirs() (files, info string, err error) {
	if n.home == "" {
		return "", "", errors.New("no home folder")
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(n.home, ".Trash"), "", nil
	case "linux":
		base := os.Getenv("XDG_DATA_HOME")
		if !filepath.IsAbs(base) {
			base = filepath.Join(n.home, ".local", "share")
		}
		return filepath.Join(base, "Trash", "files"), filepath.Join(base, "Trash", "info"), nil
	}
	return "", "", fmt.Errorf("there is no trash to move files to on %s", runtime.GOOS)
}

func (n *Node) trashFiles(paths []string, cwd string) (map[string]any, error) {
	if len(paths) == 0 {
		return nil, errors.New("no paths")
	}
	entries := make([]editEntry, 0, len(paths))
	for _, p := range paths {
		e, err := n.entry(p, cwd)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	trashed := make([]map[string]string, 0, len(entries))
	for _, e := range entries {
		id, err := n.toTrash(e.real, e.shown)
		if err != nil {
			return nil, explainRename(e.shown, err)
		}
		trashed = append(trashed, map[string]string{"path": e.shown, "trash_id": id})
	}
	n.logger.Printf("moved to the trash from the phone: %d", len(trashed))
	return map[string]any{"trashed": trashed}, nil
}

// toTrash moves real into the trash under its own name, or "name 2.ext"… when the trash has one; on Linux the
// .trashinfo is written first (it reserves the name, as the freedesktop spec says).
func (n *Node) toTrash(real, shown string) (string, error) {
	files, info, err := n.trashDirs()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(files, 0o700); err != nil {
		return "", err
	}
	if info != "" {
		if err := os.MkdirAll(info, 0o700); err != nil {
			return "", err
		}
	}
	name := filepath.Base(real)
	stem, ext := splitName(name)
	if st, err := os.Lstat(real); err == nil && st.IsDir() {
		stem, ext = name, ""
	}
	for i := 1; i < 1000; i++ {
		candidate := name
		if i > 1 {
			candidate = fmt.Sprintf("%s %d%s", stem, i, ext)
		}
		var infoFile string
		if info != "" {
			infoFile = filepath.Join(info, candidate+".trashinfo")
			f, err := os.OpenFile(infoFile, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
			if errors.Is(err, os.ErrExist) {
				continue
			}
			if err != nil {
				return "", err
			}
			_, err = fmt.Fprintf(f, "[Trash Info]\nPath=%s\nDeletionDate=%s\n", (&url.URL{Path: real}).EscapedPath(),
				time.Now().Format("2006-01-02T15:04:05"))
			if cerr := f.Close(); err == nil {
				err = cerr
			}
			if err != nil {
				_ = os.Remove(infoFile)
				return "", err
			}
		}
		err := renameExcl(real, filepath.Join(files, candidate))
		if err != nil && infoFile != "" {
			_ = os.Remove(infoFile)
		}
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		return trashID{Name: candidate, Real: real, Shown: shown}.encode(), nil
	}
	return "", fmt.Errorf("the trash already has too many called %s", name)
}

// restoreFiles puts trashed files back where they were (Undo). A name taken there meanwhile refuses that one.
func (n *Node) restoreFiles(ids []string) (map[string]any, error) {
	if len(ids) == 0 {
		return nil, errors.New("no trash_ids")
	}
	files, info, err := n.trashDirs()
	if err != nil {
		return nil, err
	}
	restored := make([]map[string]string, 0, len(ids))
	for _, s := range ids {
		t, err := decodeTrashID(s)
		if err != nil {
			return nil, err
		}
		from := filepath.Join(files, t.Name)
		if _, err := os.Lstat(from); err != nil {
			return nil, errNotFound(t.Shown)
		}
		if _, _, err := n.writableDir(filepath.Dir(t.Real), ""); err != nil {
			return nil, err
		}
		if err := n.writable(t.Real); err != nil {
			return nil, err
		}
		if err := renameExcl(from, t.Real); err != nil {
			if errors.Is(err, os.ErrExist) {
				return nil, errExists(filepath.Base(t.Real))
			}
			return nil, explainRename(t.Shown, err)
		}
		if info != "" {
			_ = os.Remove(filepath.Join(info, t.Name+".trashinfo"))
		}
		restored = append(restored, map[string]string{"path": t.Shown})
	}
	n.logger.Printf("put back from the trash from the phone: %d", len(restored))
	return map[string]any{"restored": restored}, nil
}

func (n *Node) fileEditCall(method string, params json.RawMessage) (any, error) {
	var p struct {
		Path     string   `json:"path"`
		Paths    []string `json:"paths"`
		Cwd      string   `json:"cwd"`
		Name     string   `json:"name"`
		Dir      string   `json:"dir"`
		Conflict string   `json:"conflict"`
		TrashIDs []string `json:"trash_ids"`
	}
	if err := json.Unmarshal(params, &p); err != nil {
		return nil, fmt.Errorf("bad params: %w", err)
	}
	switch method {
	case "file.rename":
		return n.renameFile(p.Path, p.Cwd, p.Name)
	case "file.move":
		return n.moveFiles(p.Paths, p.Cwd, p.Dir, p.Conflict)
	case "file.trash":
		return n.trashFiles(p.Paths, p.Cwd)
	case "file.restore":
		return n.restoreFiles(p.TrashIDs)
	}
	return nil, fmt.Errorf("unknown method %s", method)
}
