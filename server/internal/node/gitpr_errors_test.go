package node

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// These exercise git.pr through its real Git and gh parsing. Missing paths,
// non-repositories, and detached HEAD all map to git_detached today.
func TestGitPRPermanentSourceErrors(t *testing.T) {
	f := newPullFixture(t)
	missing := filepath.Join(f.node.home, "missing")
	notRepo := filepath.Join(f.node.home, "not-repo")
	if err := os.Mkdir(notRepo, 0700); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ name, dir string }{{"missing directory", missing}, {"not repository", notRepo}} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.node.gitPR(context.Background(), tc.dir)
			if err == nil {
				t.Fatal("expected error")
			}
			if code, _ := gitCodeOf(t, err); code != gitCodeDetached {
				t.Fatalf("code %s: %v", code, err)
			}
		})
	}
	t.Run("not GitHub", func(t *testing.T) {
		gh := filepath.Join(t.TempDir(), "gh")
		script := "#!/bin/sh\nprintf '%s\\n' 'none of the git remotes are GitHub hosts' >&2\nexit 1\n"
		if err := os.WriteFile(gh, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		old := ghLookPath
		ghLookPath = func() (string, error) { return gh, nil }
		defer func() { ghLookPath = old }()
		_, err := f.node.gitPR(context.Background(), f.local)
		if err == nil {
			t.Fatal("expected error")
		}
		if code, _ := gitCodeOf(t, err); code != gitCodeNotGitHub {
			t.Fatalf("code %s: %v", code, err)
		}
	})
	t.Run("detached head", func(t *testing.T) {
		runPullGit(t, f.local, "checkout", "--detach")
		_, err := f.node.gitPR(context.Background(), f.local)
		if err == nil {
			t.Fatal("expected error")
		}
		if code, _ := gitCodeOf(t, err); code != gitCodeDetached {
			t.Fatalf("code %s: %v", code, err)
		}
	})
}
