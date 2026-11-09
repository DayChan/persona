package node

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"testing"
)

// listNames lists dir with a fresh test node and returns the entries and the truncated flag.
func listNames(t *testing.T, n *Node, dir string) ([]DirEntry, bool) {
	t.Helper()
	res, err := n.listDir(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	return res["entries"].([]DirEntry), res["truncated"].(bool)
}

func writeEmpty(t *testing.T, dir string, names ...string) {
	t.Helper()
	for _, name := range names {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

// A folder over the cap still lists its subfolders first: the order is decided before the cap, not after it.
func TestListDirKeepsSubfoldersPastTheCap(t *testing.T) {
	n, home := testNode(t)
	dir := filepath.Join(home, "big")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxDirEntries + 5 {
		writeEmpty(t, dir, fmt.Sprintf("f%05d.txt", i))
	}
	if err := os.Mkdir(filepath.Join(dir, "zz-sub"), 0o700); err != nil {
		t.Fatal(err)
	}

	ents, truncated := listNames(t, n, dir)
	if !truncated || len(ents) != maxDirEntries {
		t.Fatalf("truncated=%v len=%d, want true and %d", truncated, len(ents), maxDirEntries)
	}
	if ents[0].Name != "zz-sub" || !ents[0].Dir {
		t.Fatalf("first entry = %+v, want the zz-sub directory", ents[0])
	}
	if ents[1].Name != "f00000.txt" || ents[len(ents)-1].Name != fmt.Sprintf("f%05d.txt", maxDirEntries-2) {
		t.Fatalf("files run %s .. %s", ents[1].Name, ents[len(ents)-1].Name)
	}
	if ents[1].ModTime.IsZero() || ents[len(ents)-1].ModTime.IsZero() {
		t.Fatalf("kept entries lost their file info: %+v %+v", ents[1], ents[len(ents)-1])
	}
}

// The cap follows the case-insensitive order: a lowercase name that sorts last in byte order is not cut.
func TestListDirCapsAfterTheCaseInsensitiveSort(t *testing.T) {
	n, home := testNode(t)
	dir := filepath.Join(home, "big")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	for i := range maxDirEntries {
		writeEmpty(t, dir, fmt.Sprintf("B%05d", i))
	}
	writeEmpty(t, dir, "a-first")

	ents, truncated := listNames(t, n, dir)
	if !truncated || len(ents) != maxDirEntries {
		t.Fatalf("truncated=%v len=%d, want true and %d", truncated, len(ents), maxDirEntries)
	}
	if ents[0].Name != "a-first" {
		t.Fatalf("first entry = %q, want a-first", ents[0].Name)
	}
}

// A small folder keeps the tree's order: directories first, then names case-insensitively, byte order breaking ties.
func TestListDirOrder(t *testing.T) {
	n, home := testNode(t)
	dir := filepath.Join(home, "small")
	for _, d := range []string{dir, filepath.Join(dir, "Beta"), filepath.Join(dir, "alpha")} {
		if err := os.Mkdir(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeEmpty(t, dir, "Zeta.txt", "apple.txt", "readme")
	want := []string{"alpha", "Beta"}
	if runtime.GOOS != "windows" { // a link to a directory sorts with the directories
		if err := os.Symlink(filepath.Join(dir, "alpha"), filepath.Join(dir, "Gamma")); err != nil {
			t.Fatal(err)
		}
		want = append(want, "Gamma")
	}
	want = append(want, "apple.txt")
	if _, err := os.Stat(filepath.Join(dir, "README")); err != nil { // case-sensitive file system: both names exist
		writeEmpty(t, dir, "README")
		want = append(want, "README")
	}
	want = append(want, "readme", "Zeta.txt")

	ents, truncated := listNames(t, n, dir)
	var got []string
	for _, e := range ents {
		got = append(got, e.Name)
	}
	if truncated || !slices.Equal(got, want) {
		t.Fatalf("entries = %v (truncated=%v), want %v", got, truncated, want)
	}
	for _, e := range ents {
		if e.Dir != (e.Name == "alpha" || e.Name == "Beta" || e.Name == "Gamma") {
			t.Errorf("%s: dir=%v", e.Name, e.Dir)
		}
	}
}
