package node

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

func editCall(t *testing.T, n *Node, method string, params map[string]any) (map[string]any, error) {
	t.Helper()
	raw, _ := json.Marshal(params)
	out, err := n.fileEditCall(method, raw)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(out)
	var m map[string]any
	_ = json.Unmarshal(b, &m)
	return m, nil
}

func makeFile(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}

func fileText(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func wantCode(t *testing.T, err error, code string) {
	t.Helper()
	var re *rpc.Error
	if !errors.As(err, &re) || re.Code != code {
		t.Fatalf("want %s, got %v", code, err)
	}
}

func notThere(t *testing.T, path string) {
	t.Helper()
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("%s is still there (%v)", path, err)
	}
}

// A rename stays in its folder, refuses a taken name without touching either file, renames a link as the link, and
// a case-only change works on a disk that ignores case.
func TestRenameFile(t *testing.T) {
	n, home := writeNode(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "a")
	makeFile(t, filepath.Join(home, "proj", "b.txt"), "b")
	out, err := editCall(t, n, "file.rename", map[string]any{"path": "~/proj/a.txt", "name": "c.txt"})
	if err != nil || out["path"] != "~/proj/c.txt" {
		t.Fatalf("rename: %v %v", out, err)
	}
	if fileText(t, filepath.Join(home, "proj", "c.txt")) != "a" {
		t.Fatal("c.txt is not the old a.txt")
	}
	notThere(t, filepath.Join(home, "proj", "a.txt"))

	_, err = editCall(t, n, "file.rename", map[string]any{"path": "c.txt", "cwd": home + "/proj", "name": "b.txt"})
	wantCode(t, err, proto.CodeExists)
	if fileText(t, filepath.Join(home, "proj", "b.txt")) != "b" || fileText(t, filepath.Join(home, "proj", "c.txt")) != "a" {
		t.Fatal("a refused rename changed a file")
	}

	for _, bad := range []string{"", "..", "x/y", "a\x00b"} {
		if _, err := editCall(t, n, "file.rename", map[string]any{"path": "~/proj/c.txt", "name": bad}); err == nil {
			t.Errorf("name %q was taken", bad)
		}
	}
	_, err = editCall(t, n, "file.rename", map[string]any{"path": "~/proj/nope.txt", "name": "x"})
	wantCode(t, err, proto.CodeNotFound)

	target := filepath.Join(home, "proj", "b.txt")
	if err := os.Symlink(target, filepath.Join(home, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := editCall(t, n, "file.rename", map[string]any{"path": "~/link", "name": "link2"}); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Lstat(filepath.Join(home, "link2")); err != nil || st.Mode()&os.ModeSymlink == 0 {
		t.Fatalf("the link was not renamed as a link: %v", err)
	}
	if fileText(t, target) != "b" {
		t.Fatal("the link's target changed")
	}

	if _, err := editCall(t, n, "file.rename", map[string]any{"path": "~/proj/c.txt", "name": "C.txt"}); err != nil {
		t.Fatalf("case-only rename: %v", err)
	}
	ents, _ := os.ReadDir(filepath.Join(home, "proj"))
	var names []string
	for _, e := range ents {
		names = append(names, e.Name())
	}
	if !strings.Contains(strings.Join(names, ","), "C.txt") {
		t.Fatalf("after a case-only rename: %v", names)
	}
}

// Nothing outside what an upload may write is renamed, moved or trashed: Persona's folders, the Library, the home
// itself, anything outside the home, and not into those either.
func TestFileEditStaysInTheHome(t *testing.T) {
	n, home := writeNode(t)
	outside := t.TempDir()
	makeFile(t, filepath.Join(outside, "o.txt"), "o")
	makeFile(t, filepath.Join(home, ".persona", "hub.json"), "{}")
	makeFile(t, filepath.Join(home, "Library", "x.plist"), "x")
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "a")
	if err := os.Symlink(outside, filepath.Join(home, "out")); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{"~", "~/.persona", "~/.persona/hub.json", "~/Library", "~/Library/x.plist",
		filepath.Join(outside, "o.txt"), "~/out/o.txt", "~/proj/../.."} {
		refused := func(err error) { // read_only, or the read guard's own refusal first (hub.json holds secrets)
			t.Helper()
			var re *rpc.Error
			if err == nil || p != "~/.persona/hub.json" && (!errors.As(err, &re) || re.Code != proto.CodeReadOnly) {
				t.Fatalf("%s: %v", p, err)
			}
		}
		_, err := editCall(t, n, "file.rename", map[string]any{"path": p, "name": "z"})
		refused(err)
		_, err = editCall(t, n, "file.trash", map[string]any{"paths": []string{p}})
		refused(err)
		_, err = editCall(t, n, "file.move", map[string]any{"paths": []string{p}, "dir": "~/proj"})
		refused(err)
	}
	for _, d := range []string{"~/.persona", "~/Library", outside, "~/out"} {
		_, err := editCall(t, n, "file.move", map[string]any{"paths": []string{"~/proj/a.txt"}, "dir": d})
		wantCode(t, err, proto.CodeReadOnly)
	}
	_, err := editCall(t, n, "file.rename", map[string]any{"path": "~/proj", "name": "../escape"})
	if err == nil {
		t.Fatal("a name with a slash was taken")
	}
	// Renaming into a name the guard keeps (~/proj → ~/Library would be one)
	if err := os.RemoveAll(filepath.Join(home, "Library")); err != nil {
		t.Fatal(err)
	}
	_, err = editCall(t, n, "file.rename", map[string]any{"path": "~/proj", "name": "Library"})
	wantCode(t, err, proto.CodeReadOnly)
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "a" || fileText(t, filepath.Join(outside, "o.txt")) != "o" {
		t.Fatal("a refused change touched a file")
	}
}

// A move refuses a taken name before anything moves, keeps both or replaces (the replaced one goes to the trash),
// and never moves a folder into itself.
func TestMoveFiles(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	n, home := writeNode(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "new a")
	makeFile(t, filepath.Join(home, "proj", "b.txt"), "b")
	makeFile(t, filepath.Join(home, "dest", "a.txt"), "old a")
	paths := []string{"~/proj/b.txt", "~/proj/a.txt"}

	_, err := editCall(t, n, "file.move", map[string]any{"paths": paths, "dir": "~/dest"})
	wantCode(t, err, proto.CodeExists)
	if fileText(t, filepath.Join(home, "proj", "b.txt")) != "b" {
		t.Fatal("a refused move moved b.txt anyway")
	}

	out, err := editCall(t, n, "file.move", map[string]any{"paths": paths, "dir": "~/dest", "conflict": "keep"})
	if err != nil {
		t.Fatal(err)
	}
	moved, _ := json.Marshal(out["moved"])
	if string(moved) != `[{"from":"~/proj/b.txt","to":"~/dest/b.txt"},{"from":"~/proj/a.txt","to":"~/dest/a 2.txt"}]` {
		t.Fatalf("moved: %s", moved)
	}
	if fileText(t, filepath.Join(home, "dest", "a.txt")) != "old a" || fileText(t, filepath.Join(home, "dest", "a 2.txt")) != "new a" {
		t.Fatal("keep did not keep both")
	}

	makeFile(t, filepath.Join(home, "proj", "b.txt"), "newer b")
	if _, err := editCall(t, n, "file.move", map[string]any{"paths": []string{"~/proj/b.txt"}, "dir": "~/dest", "conflict": "replace"}); err != nil {
		t.Fatal(err)
	}
	if fileText(t, filepath.Join(home, "dest", "b.txt")) != "newer b" {
		t.Fatal("replace did not replace")
	}
	files, _, _ := n.trashDirs()
	if fileText(t, filepath.Join(files, "b.txt")) != "b" {
		t.Fatal("the replaced file is not in the trash")
	}

	makeFile(t, filepath.Join(home, "dest", "sub", "x"), "x")
	_, err = editCall(t, n, "file.move", map[string]any{"paths": []string{"~/dest"}, "dir": "~/dest/sub"})
	if err == nil || !strings.Contains(err.Error(), "into itself") {
		t.Fatalf("a folder moved into itself: %v", err)
	}
	if out, err := editCall(t, n, "file.move", map[string]any{"paths": []string{"~/dest/sub"}, "dir": "~/dest"}); err != nil {
		t.Fatalf("a move to where it already is: %v %v", out, err)
	}
	if _, err := editCall(t, n, "file.move", map[string]any{"paths": []string{"~/dest/sub"}, "dir": "~"}); err != nil {
		t.Fatal(err)
	}
	if fileText(t, filepath.Join(home, "sub", "x")) != "x" {
		t.Fatal("the folder did not move with what is in it")
	}
}

// A replace whose rename fails (the source went away after the request checked it) puts the destination back instead
// of leaving it in the trash (#1968).
func TestReplaceMoveFailureKeepsDestination(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	n, home := writeNode(t)
	makeFile(t, filepath.Join(home, "proj", "report.txt"), "new")
	makeFile(t, filepath.Join(home, "dest", "report.txt"), "original destination")
	e, err := n.entry("~/proj/report.txt", "")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.real); err != nil { // another program moved it meanwhile
		t.Fatal(err)
	}
	if _, err := n.moveOne(e, filepath.Join(home, "dest"), "report.txt", proto.UploadConflictReplace); err == nil {
		t.Fatal("a move of a missing source succeeded")
	}
	if got := fileText(t, filepath.Join(home, "dest", "report.txt")); got != "original destination" {
		t.Fatalf("destination after a failed replace: %q", got)
	}
	files, info, _ := n.trashDirs()
	notThere(t, filepath.Join(files, "report.txt"))
	if info != "" {
		notThere(t, filepath.Join(info, "report.txt.trashinfo"))
	}
}

// Trash and restore round-trip through the id alone (a fresh node puts it back), a name the trash already has gets a
// number, Linux writes a .trashinfo, and a name taken meanwhile refuses the restore.
func TestTrashAndRestore(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	n, home := writeNode(t)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "first")
	makeFile(t, filepath.Join(home, "proj", "dir", "in.txt"), "in")
	out, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{"a.txt", "dir"}, "cwd": home + "/proj"})
	if err != nil {
		t.Fatal(err)
	}
	trashed := out["trashed"].([]any)
	if len(trashed) != 2 || trashed[0].(map[string]any)["path"] != "a.txt" {
		t.Fatalf("trashed: %v", out)
	}
	notThere(t, filepath.Join(home, "proj", "a.txt"))
	notThere(t, filepath.Join(home, "proj", "dir"))
	files, info, _ := n.trashDirs()
	if fileText(t, filepath.Join(files, "a.txt")) != "first" {
		t.Fatal("a.txt is not in the trash")
	}
	if runtime.GOOS == "linux" {
		ti := fileText(t, filepath.Join(info, "a.txt.trashinfo"))
		if !strings.Contains(ti, "[Trash Info]\nPath="+filepath.Join(home, "proj", "a.txt")) || !strings.Contains(ti, "DeletionDate=") {
			t.Fatalf("trashinfo: %q", ti)
		}
	}

	makeFile(t, filepath.Join(home, "proj", "a.txt"), "second")
	out2, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{"~/proj/a.txt"}})
	if err != nil {
		t.Fatal(err)
	}
	if fileText(t, filepath.Join(files, "a 2.txt")) != "second" {
		t.Fatal("the second a.txt did not get its own name in the trash")
	}

	fresh := &Node{dir: t.TempDir(), home: home, logger: n.logger}
	var ids []string
	for _, x := range trashed {
		ids = append(ids, x.(map[string]any)["trash_id"].(string))
	}
	res, err := editCall(t, fresh, "file.restore", map[string]any{"trash_ids": ids})
	if err != nil {
		t.Fatal(err)
	}
	if b, _ := json.Marshal(res["restored"]); string(b) != `[{"path":"a.txt"},{"path":"dir"}]` {
		t.Fatalf("restored: %s", b)
	}
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "first" || fileText(t, filepath.Join(home, "proj", "dir", "in.txt")) != "in" {
		t.Fatal("not put back")
	}
	if info != "" {
		notThere(t, filepath.Join(info, "a.txt.trashinfo"))
	}

	second := out2["trashed"].([]any)[0].(map[string]any)["trash_id"].(string)
	_, err = editCall(t, n, "file.restore", map[string]any{"trash_ids": []string{second}})
	wantCode(t, err, proto.CodeExists)
	_, err = editCall(t, n, "file.restore", map[string]any{"trash_ids": ids[:1]})
	wantCode(t, err, proto.CodeNotFound)
}

// A made-up trash id can't take a file out of the trash into a place the guard keeps, or reach outside the trash.
func TestRestoreChecksTheGuard(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", "")
	n, home := writeNode(t)
	files, _, _ := n.trashDirs()
	makeFile(t, filepath.Join(files, "evil"), "e")
	makeFile(t, filepath.Join(home, ".persona", "keep"), "k")
	for _, id := range []trashID{
		{Name: "evil", Real: filepath.Join(home, ".persona", "evil"), Shown: "x"},
		{Name: "evil", Real: filepath.Join(t.TempDir(), "evil"), Shown: "x"},
		{Name: "../../proj", Real: filepath.Join(home, "stolen"), Shown: "x"},
	} {
		if _, err := editCall(t, n, "file.restore", map[string]any{"trash_ids": []string{id.encode()}}); err == nil {
			t.Errorf("restored %+v", id)
		}
	}
	if _, err := editCall(t, n, "file.restore", map[string]any{"trash_ids": []string{"not base64!"}}); err == nil {
		t.Error("a bad id was taken")
	}
	if fileText(t, filepath.Join(files, "evil")) != "e" {
		t.Fatal("the trashed file moved")
	}
}

// A trash batch that fails partway puts back what it already moved instead of leaving it in the trash with no id to
// undo it by (#3123): on Linux a 255-byte name can't get its .trashinfo.
func TestTrashBatchFailurePutsBack(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("the .trashinfo name limit is Linux's")
	}
	t.Setenv("XDG_DATA_HOME", "")
	n, home := writeNode(t)
	long := strings.Repeat("l", 255)
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "a")
	makeFile(t, filepath.Join(home, "proj", long), "long")
	_, err := editCall(t, n, "file.trash", map[string]any{"paths": []string{"a.txt", long}, "cwd": home + "/proj"})
	if err == nil || !strings.Contains(err.Error(), "too long for the trash") {
		t.Fatalf("a batch with a name too long for the trash: %v", err)
	}
	if fileText(t, filepath.Join(home, "proj", "a.txt")) != "a" || fileText(t, filepath.Join(home, "proj", long)) != "long" {
		t.Fatal("a failed trash batch left files out of place")
	}
	files, info, _ := n.trashDirs()
	for _, dir := range []string{files, info} {
		left, err := os.ReadDir(dir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if len(left) != 0 {
			t.Fatalf("left in %s: %v", dir, left)
		}
	}

	// What can't go back (its name was taken meanwhile) is named, with the ids the app needs to offer Undo.
	id, err := n.toTrash(filepath.Join(home, "proj", "a.txt"), "a.txt")
	if err != nil {
		t.Fatal(err)
	}
	makeFile(t, filepath.Join(home, "proj", "a.txt"), "new a")
	err = n.undoTrash([]map[string]string{{"path": "a.txt", "trash_id": id}}, errors.New("b.txt failed"))
	var re *rpc.Error
	if !errors.As(err, &re) || re.Message != "b.txt failed; a.txt is still in the trash" {
		t.Fatalf("a restore that failed: %v", err)
	}
	if want, _ := json.Marshal(map[string]any{"trashed": []map[string]string{{"path": "a.txt", "trash_id": id}}}); string(re.Data) != string(want) {
		t.Fatalf("data: %s", re.Data)
	}
	if fileText(t, filepath.Join(files, "a.txt")) != "a" || fileText(t, filepath.Join(home, "proj", "a.txt")) != "new a" {
		t.Fatal("a refused restore moved something")
	}
}
