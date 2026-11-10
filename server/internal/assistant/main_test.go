package assistant

import (
	"fmt"
	"os"
	"testing"

	"github.com/nwverse/persona/server/internal/db/dbtest"
	"github.com/nwverse/persona/server/internal/testhome"
)

// The tests that need Postgres run on a database of their own (dbtest, #583); without PERSONA_TEST_DATABASE_URL they
// skip. Run with a throw-away HOME (testhome): nothing in the package may write the default ~/.persona. Every inbox
// list checks the item bytes GET /api/v1/inbox kept against a fresh encoding (inboxMemoVerify): a change of an item
// that forgets nothing (touched, chainAdd, saveInboxPrepared) fails the run.
func TestMain(m *testing.M) {
	inboxMemoVerify = true
	os.Exit(testhome.Main(func() int { return staleListedCheck(dbtest.Main(m)) }))
}

// staleListedCheck fails a run in which a GET listed an item from bytes that were not its encoding any more.
func staleListedCheck(code int) int {
	notes := inboxMemoStale.taken()
	for _, n := range notes {
		fmt.Fprintln(os.Stderr, n)
	}
	if len(notes) > 0 && code == 0 {
		fmt.Fprintf(os.Stderr, "FAIL: %d inbox GETs listed stale item bytes (inbox_list_memo.go)\n", len(notes))
		return 1
	}
	return code
}

// The package's tests never see the developer's HOME or PERSONA_HOME (testhome); most of them need a database and skip
// in CI, this one keeps the TestMain wiring from being dropped there.
func TestTestsRunInAThrowAwayHome(t *testing.T) {
	if !testhome.Active() || os.Getenv("PERSONA_HOME") != "" {
		t.Fatalf("not isolated: active %v, PERSONA_HOME %q", testhome.Active(), os.Getenv("PERSONA_HOME"))
	}
}
