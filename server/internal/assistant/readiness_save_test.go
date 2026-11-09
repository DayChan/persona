package assistant

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

// stuckProbeStore is a database that never answers the first write of a probe's result.
type stuckProbeStore struct {
	*memStore
	stuck    atomic.Bool
	deadline atomic.Bool // the stuck write had a deadline
}

func (s *stuckProbeStore) PutSetting(ctx context.Context, key string, v any) error {
	if rec, ok := v.(readinessRecord); ok && key == readinessKey && rec.Probe != nil && s.stuck.CompareAndSwap(false, true) {
		_, has := ctx.Deadline()
		s.deadline.Store(has)
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(5 * time.Second):
			return errors.New("the database never answered")
		}
	}
	return s.memStore.PutSetting(ctx, key, v)
}

// A probe result the database does not take gives up at historyTimeout: the check runs detached from the client
// (#1892), so without its own deadline the write, and the readiness lock it holds, would wait for good.
func TestReadinessProbeSaveIsBounded(t *testing.T) {
	shortHistoryTimeout(t, 100*time.Millisecond)
	f := newFakeHub().withMachine()
	f.doctor = map[string]string{"server": "2.1.308，支持 --plugin-dir"}
	f.settings = &Settings{Main: "m1"}
	st := &stuckProbeStore{memStore: &memStore{}}
	s := newServiceOn(t, f, st)
	start := time.Now()
	r := getReadiness(t, s, "probe=1", "")
	if took := time.Since(start); took > 2*time.Second || !st.stuck.Load() || !st.deadline.Load() {
		t.Fatalf("the probe took %v (stuck write seen %v, with a deadline %v)", took, st.stuck.Load(), st.deadline.Load())
	}
	if r.Probe == nil || !r.Probe.OK {
		t.Fatalf("the check's own result is answered when it could not be kept: %+v", r.Probe)
	}
	done := make(chan struct{})
	go func() { defer close(done); getReadiness(t, s, "", "") }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("readiness waits behind the stuck probe save")
	}
}
