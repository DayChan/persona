package node

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// otherGh is a program that is also named gh but is not the GitHub CLI: it answers --version with a usage line and
// prints something for any other call.
const otherGh = `echo "usage: gh [-h] [target ...]" >&2; exit 2`

func TestLookGitHubCLISkipsAnotherGh(t *testing.T) {
	other, real := t.TempDir(), t.TempDir()
	writeGh(t, other, otherGh)
	want := writeGh(t, real, ghVersionLine+"exit 0")
	t.Setenv("PATH", strings.Join([]string{"relative/bin", other, real}, string(os.PathListSeparator)))
	if got, err := lookGitHubCLI(); err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestLookGitHubCLIFallbackDirs(t *testing.T) {
	other, brew := t.TempDir(), t.TempDir()
	writeGh(t, other, otherGh)
	t.Setenv("PATH", other)
	if _, err := lookGitHubCLI(); !errors.Is(err, errNotGitHubCLI) {
		t.Fatalf("only another gh: %v", err)
	}
	want := writeGh(t, brew, ghVersionLine+"exit 0")
	old := ghFallbackDirs
	ghFallbackDirs = func() []string { return []string{brew} }
	t.Cleanup(func() { ghFallbackDirs = old })
	if got, err := lookGitHubCLI(); err != nil || got != want {
		t.Fatalf("got %q, %v; want %q", got, err, want)
	}
}

func TestLookGitHubCLINone(t *testing.T) {
	dir := t.TempDir()
	// Not executable, and a directory named gh: neither is a candidate.
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte("#!/bin/sh\n"+ghVersionLine), 0o644); err != nil {
		t.Fatal(err)
	}
	sub := t.TempDir()
	if err := os.Mkdir(filepath.Join(sub, "gh"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+sub)
	if _, err := lookGitHubCLI(); !errors.Is(err, exec.ErrNotFound) {
		t.Fatalf("no gh: %v", err)
	}
}

// An answer is kept while the file is unchanged and asked again once it changes (gh installed over another program).
func TestLookGitHubCLIRemembersUntilChanged(t *testing.T) {
	dir := t.TempDir()
	count := filepath.Join(t.TempDir(), "count")
	p := writeGh(t, dir, "echo x >> "+count+"\n"+otherGh)
	t.Setenv("PATH", dir)
	for range 3 {
		if _, err := lookGitHubCLI(); !errors.Is(err, errNotGitHubCLI) {
			t.Fatalf("another gh: %v", err)
		}
	}
	if b, _ := os.ReadFile(count); strings.Count(string(b), "x") != 1 {
		t.Fatalf("asked %d times, want once", strings.Count(string(b), "x"))
	}
	writeGh(t, dir, ghVersionLine+"# the GitHub CLI, installed over it\nexit 0")
	if got, err := lookGitHubCLI(); err != nil || got != p {
		t.Fatalf("after the change: %q, %v", got, err)
	}
}

// A program that ran and said it is not the GitHub CLI is remembered; one that could not start is asked again.
func TestLookGitHubCLIKeepsOnlyAnswersFromARun(t *testing.T) {
	ranDir, failedDir := t.TempDir(), t.TempDir()
	ran := writeGh(t, ranDir, otherGh)
	// Executable but neither a binary nor a script with #!: the exec itself fails (ENOEXEC).
	failed := filepath.Join(failedDir, "gh")
	if err := os.WriteFile(failed, []byte("not a program\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", ranDir+string(os.PathListSeparator)+failedDir)
	if _, err := lookGitHubCLI(); !errors.Is(err, errNotGitHubCLI) {
		t.Fatalf("no GitHub CLI: %v", err)
	}
	ghAnswers.Lock()
	a, kept := ghAnswers.m[ran]
	_, keptFailed := ghAnswers.m[failed]
	ghAnswers.Unlock()
	if !kept || a.cli {
		t.Fatalf("the answer of a program that ran: %+v, kept %v", a, kept)
	}
	if keptFailed {
		t.Fatal("a program that could not start was remembered")
	}
}

// ghToken and ghRun say "no gh" for another program named gh, never run it for a token, and use the GitHub CLI behind it.
func TestGhTokenSkipsAnotherGh(t *testing.T) {
	ctx := context.Background()
	other, real := t.TempDir(), t.TempDir()
	ran := filepath.Join(t.TempDir(), "ran")
	writeGh(t, other, `[ "$1" = auth ] && touch `+ran+"\n"+otherGh)
	t.Setenv("PATH", other)
	var re *rpc.Error
	if _, err := ghToken(ctx, ""); !errors.As(err, &re) || re.Code != proto.CodeNoGh {
		t.Fatalf("only another gh: %v", err)
	}
	if _, err := ghRun(ctx, t.TempDir(), ghVersionTimeout, "pr", "view"); !errors.As(err, &re) || re.Code != proto.CodeNoGh {
		t.Fatalf("ghRun with only another gh: %v", err)
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("another gh was asked for a token")
	}
	writeGh(t, real, ghVersionLine+`[ "$1 $2" = "auth token" ] && echo gho_example0000000000000000000000000000`)
	t.Setenv("PATH", other+string(os.PathListSeparator)+real)
	if out, err := ghToken(ctx, ""); err != nil || out["token"] != "gho_example0000000000000000000000000000" {
		t.Fatalf("%v %v", out, err)
	}
}
