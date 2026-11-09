package serve

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/connector"
)

func TestRecallDoResultMapsTheCoreOutcome(t *testing.T) {
	for _, tc := range []struct {
		err  error
		want string
	}{
		{nil, RecallDone},
		{connector.ErrCannotRecall, RecallNotRecallable},
		{connector.ErrRecallUnavailable, RecallUnavailable},
		{connector.ErrRecallUnknown, RecallUnknown},
		{errors.New("the service said no"), RecallRefused},
	} {
		res := recallDoResult(tc.err)
		if res.Status != tc.want || tc.err != nil && res.Error == "" || tc.err == nil && res.Error != "" {
			t.Errorf("%v: %+v, want %s", tc.err, res, tc.want)
		}
	}
}

// One request id shares an in-flight failure, but does not cache it forever. A later explicit attempt can succeed;
// that success (and already_recalled) is then replayed without calling the connector again.
func TestRecallRunsShareFailuresAndCacheSuccess(t *testing.T) {
	var runs recallRuns
	var calls atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	runUnknown := func() RecallDoResult {
		calls.Add(1)
		once.Do(func() { close(entered) })
		<-release
		return RecallDoResult{Status: RecallUnknown}
	}
	first := make(chan RecallDoResult, 1)
	go func() { first <- runs.do("row", runUnknown) }()
	<-entered
	second := make(chan RecallDoResult, 1)
	go func() {
		second <- runs.do("row", func() RecallDoResult {
			calls.Add(1)
			return RecallDoResult{Status: RecallDone}
		})
	}()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(time.Second)
	defer timeout.Stop()
	for {
		runs.mu.Lock()
		cur := runs.runs["row"]
		joined := cur != nil && cur.waiters == 1
		runs.mu.Unlock()
		if joined {
			break
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			close(release)
			t.Fatal("the second recall did not join the first")
		}
	}
	close(release)
	if a, b := <-first, <-second; a.Status != RecallUnknown || b.Status != RecallUnknown || calls.Load() != 1 {
		t.Fatalf("in-flight answers %v %v, calls %d", a, b, calls.Load())
	}
	if res := runs.do("row", func() RecallDoResult { calls.Add(1); return RecallDoResult{Status: RecallDone} }); res.Status != RecallDone || calls.Load() != 2 {
		t.Fatalf("the retry: %v, calls %d", res, calls.Load())
	}
	if res := runs.do("row", func() RecallDoResult { calls.Add(1); return RecallDoResult{Status: RecallUnknown} }); res.Status != RecallDone || calls.Load() != 2 {
		t.Fatalf("the cached success: %v, calls %d", res, calls.Load())
	}
	if res := runs.do("already", func() RecallDoResult { return RecallDoResult{Status: RecallAlreadyRecalled} }); res.Status != RecallAlreadyRecalled {
		t.Fatal(res)
	}
	if res := runs.do("already", func() RecallDoResult { t.Fatal("an already-recalled success was not cached"); return RecallDoResult{} }); res.Status != RecallAlreadyRecalled {
		t.Fatal(res)
	}
}
