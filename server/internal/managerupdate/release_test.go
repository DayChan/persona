package managerupdate

import (
	"io"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/update"
)

func TestStageAndMismatchedReleaseCannotActivate(t *testing.T) {
	c, p := fixture(t)
	for _, build := range []update.Build{
		{Version: p.Version, Commit: p.Source, Number: 1, Channel: update.Dev},
		{Version: p.Version, Commit: p.Source, Number: 0, Channel: update.Release},
		{Version: p.Version, Commit: strings.Repeat("c", 40), Number: 1, Channel: update.Release},
		{Version: "stage-example", Commit: p.Source, Number: 1, Channel: update.Release},
	} {
		if err := validateRelease(p, build, c.Exe); err == nil {
			t.Fatalf("accepted nonmatching release: %+v", build)
		}
	}
}

func TestUpdateHelpDoesNotRequirePlan(t *testing.T) {
	if err := Main(t.Context(), update.Build{}, []string{"check", "--help"}, io.Discard); err != nil {
		t.Fatal(err)
	}
}
