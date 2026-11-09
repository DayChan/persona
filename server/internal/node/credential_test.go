package node

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fakeGh puts a gh on the PATH that answers --version as the GitHub CLI and otherwise runs script.
func fakeGh(t *testing.T, script string) {
	dir := t.TempDir()
	writeGh(t, dir, ghVersionLine+script)
	t.Setenv("PATH", dir)
}

// ghVersionLine makes a fake gh say it is the GitHub CLI.
const ghVersionLine = `[ "$1" = --version ] && { echo "gh version 2.0.0 (test)"; exit 0; }` + "\n"

// writeGh writes a gh running script into dir.
func writeGh(t *testing.T, dir, script string) string {
	t.Helper()
	p := filepath.Join(dir, "gh")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestGhToken(t *testing.T) {
	ctx := context.Background()
	code := func(err error) string {
		var re *rpc.Error
		if !errors.As(err, &re) {
			t.Fatalf("not an rpc.Error: %v", err)
		}
		return re.Code
	}

	fakeGh(t, `[ "$1 $2 $3 $4" = "auth token --hostname github.com" ] && [ "$GH_PROMPT_DISABLED" = 1 ] && echo gho_example0000000000000000000000000000`)
	out, err := ghToken(ctx, "")
	if err != nil || out["token"] != "gho_example0000000000000000000000000000" {
		t.Fatalf("%v %v", out, err)
	}

	fakeGh(t, `echo "no oauth token found for example.com" >&2; exit 1`)
	if _, err := ghToken(ctx, "example.com"); code(err) != proto.CodeNotSignedIn {
		t.Fatalf("not signed in: %v", err)
	}
	fakeGh(t, `exit 0`)
	if _, err := ghToken(ctx, "github.com"); code(err) != proto.CodeNotSignedIn {
		t.Fatalf("empty token: %v", err)
	}

	t.Setenv("PATH", t.TempDir())
	if _, err := ghToken(ctx, "github.com"); code(err) != proto.CodeNoGh {
		t.Fatalf("no gh: %v", err)
	}
	if _, err := ghToken(ctx, "github.com; rm -rf /"); err == nil {
		t.Fatal("a bad host was run")
	}
}
