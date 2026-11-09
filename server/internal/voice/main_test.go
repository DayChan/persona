package voice

import (
	"os"
	"testing"
	"time"
)

func TestMain(m *testing.M) {
	// The insertion gate's waits are measured for a person and a model (gate.go); a test's model is a script that
	// answers at once. The tests of the gate set them back.
	delegationQuiet, speechQuiet, replyAudioIn = 50*time.Millisecond, 50*time.Millisecond, 50*time.Millisecond
	// A test's model says nothing it is not scripted to, so it would owe answers (nudge.go); a nudge, which the tests
	// of other things do not expect, is the tests of nudges' to bring on (nudge_test.go sets it back).
	nudgeAfter = time.Hour
	os.Exit(m.Run())
}
