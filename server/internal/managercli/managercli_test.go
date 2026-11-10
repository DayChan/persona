package managercli

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/manager"
	"github.com/nwverse/persona/server/internal/update"
)

// A flag mistake in a manager command ends with exit 2 and, on stderr, the flag's message and then the usage; a bare
// usage mistake prints the usage only, -h exits 0 with nothing more, and any other error is the caller's.
func TestManagerExit(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", filepath.Join(home, ".persona"))
	var stdout bytes.Buffer
	err := manager.Main(context.Background(), update.Build{}, []string{"capabilities", "--bogus"}, &stdout)
	var stderr bytes.Buffer
	code, done := managerExit(err, &stderr)
	if !done || code != 2 || stdout.Len() != 0 {
		t.Fatalf("capabilities --bogus: exit %d (done %v), stdout %q; want exit 2 and nothing on stdout", code, done, stdout.String())
	}
	if got := stderr.String(); !strings.HasPrefix(got, "flag provided but not defined: -bogus\n") || !strings.Contains(got, "manager join") {
		t.Fatalf("stderr %q: want the flag's message, then the usage", got)
	}

	stderr.Reset()
	if code, done := managerExit(manager.ErrUsage, &stderr); !done || code != 2 || !strings.HasPrefix(stderr.String(), "usage:\n") {
		t.Fatalf("bare usage mistake: exit %d (done %v), stderr %q; want exit 2 and the usage only", code, done, stderr.String())
	}
	stderr.Reset()
	if code, done := managerExit(manager.ErrHelp, &stderr); !done || code != 0 || stderr.Len() != 0 {
		t.Fatalf("help: exit %d (done %v), stderr %q; want exit 0 and nothing", code, done, stderr.String())
	}
	if _, done := managerExit(errors.New("boom"), &stderr); done || stderr.Len() != 0 {
		t.Fatalf("another error was handled here (stderr %q); it is the caller's", stderr.String())
	}
}
