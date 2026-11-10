package node

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// renameIfFree is renameExcl's fallback where the system has no rename that refuses to replace, so it is tested here
// directly, whatever the system.
func TestRenameIfFreeMoves(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	if err := os.WriteFile(from, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameIfFree(from, to); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(to); err != nil || string(b) != "a" {
		t.Fatalf("destination: %q %v", b, err)
	}
	if _, err := os.Lstat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the source is still there: %v", err)
	}
}

func TestRenameIfFreeRefusesATakenName(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	if err := os.WriteFile(from, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(to, []byte("b"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameIfFree(from, to); !errors.Is(err, os.ErrExist) {
		t.Fatalf("got %v, want os.ErrExist", err)
	}
	if b, _ := os.ReadFile(to); string(b) != "b" {
		t.Fatalf("the destination was replaced: %q", b)
	}
	if b, _ := os.ReadFile(from); string(b) != "a" {
		t.Fatalf("the source changed: %q", b)
	}
}

// A file that appears at the destination after the check (#3122) is not replaced either.
func TestRenameIfFreeKeepsAFileCreatedMeanwhile(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a.txt"), filepath.Join(dir, "b.txt")
	if err := os.WriteFile(from, []byte("a"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { renameFreeCheckedHook = nil })
	renameFreeCheckedHook = func() {
		if err := os.WriteFile(to, []byte("meanwhile"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := renameIfFree(from, to); !errors.Is(err, os.ErrExist) {
		t.Fatalf("got %v, want os.ErrExist", err)
	}
	if b, _ := os.ReadFile(to); string(b) != "meanwhile" {
		t.Fatalf("the file created meanwhile was replaced: %q", b)
	}
	if b, _ := os.ReadFile(from); string(b) != "a" {
		t.Fatalf("the source changed: %q", b)
	}
}

func TestRenameIfFreeMovesAFolder(t *testing.T) {
	dir := t.TempDir()
	from, to := filepath.Join(dir, "a"), filepath.Join(dir, "b")
	if err := os.MkdirAll(filepath.Join(from, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(from, "sub", "f"), []byte("f"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := renameIfFree(from, to); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(to, "sub", "f")); err != nil || string(b) != "f" {
		t.Fatalf("moved folder: %q %v", b, err)
	}
	if _, err := os.Lstat(from); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("the source is still there: %v", err)
	}
}
