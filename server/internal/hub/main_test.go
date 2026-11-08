package hub

import (
	"context"
	"log"
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/testhome"
	"github.com/nwverse/persona/server/internal/update"
)

// TestMain runs the tests on a database of their own (dbtest, #583); re-run with PERSONA_TEST_ASSISTANT=1 the test
// binary is the assistant's process (TestAssistantProcessIsSupervised), and as the processes of the listening-socket
// tests (listenHelpers, listen_unix_test.go) with theirs.
func TestMain(m *testing.M) {
	listenHelpers()
	if os.Getenv("PERSONA_TEST_ASSISTANT") == "1" {
		if err := assistant.Main(context.Background(), update.Build{Version: "test"}, os.Stdin); err != nil {
			log.Fatal(err)
		}
		return
	}
	os.Exit(testhome.Main(func() int { return dbtest.Main(m) }))
}

// The package's tests never see the developer's HOME or PERSONA_HOME (testhome); the two tests that start the
// assistant need a database and skip in CI, this one keeps the TestMain wiring from being dropped there.
func TestTestsRunInAThrowAwayHome(t *testing.T) {
	if !testhome.Active() || os.Getenv("PERSONA_HOME") != "" {
		t.Fatalf("not isolated: active %v, PERSONA_HOME %q", testhome.Active(), os.Getenv("PERSONA_HOME"))
	}
}
