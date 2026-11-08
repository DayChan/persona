package kb

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func dirtyProject(t *testing.T, e *Engine, slug string) {
	t.Helper()
	if err := e.Store.PutProject(&Project{Slug: slug, Name: "项目 " + slug, Status: "active", Dirty: true}); err != nil {
		t.Fatal(err)
	}
}

// A project whose rewrite fails waits like a chat whose compression failed, longer with each failure in a row, and a
// success clears that. It used to be sent again every round: a model name the endpoint did not serve answered 404
// every two minutes (#1081).
func TestRefreshProjectsBacksOffAfterFailures(t *testing.T) {
	ctx := context.Background()
	m := &failingModel{err: errors.New(`ai: HTTP 404: {"error":{"message":"unknown model"}}`)}
	e := testEngine(t, m)
	var logs []string
	e.Logf = func(f string, a ...any) { logs = append(logs, fmt.Sprintf(f, a...)) }
	dirtyProject(t, e, "a")

	for i, want := range []time.Duration{compressRetry, 2 * compressRetry, 4 * compressRetry} {
		start, calls := time.Now(), m.calls
		if n := e.RefreshProjects(ctx, 3); n != 0 || m.calls != calls+1 {
			t.Fatalf("failure %d: refreshed %d with %d call(s), want 0 with 1", i+1, n, m.calls-calls)
		}
		r := e.projectRetries["a"]
		if wait := r.at.Sub(start); r.failures != i+1 || wait < want || wait > want+time.Minute {
			t.Fatalf("failure %d: %d failure(s), next try in %v; want %d, %v", i+1, r.failures, wait, i+1, want)
		}
		if n := e.RefreshProjects(ctx, 3); n != 0 || m.calls != calls+1 {
			t.Fatalf("failure %d: a round before the next try sent it again", i+1)
		}
		r.at = time.Now().Add(-time.Second) // the wait is over
		e.projectRetries["a"] = r
	}
	if len(logs) != 3 || !strings.Contains(logs[2], "failed 3 time(s) in a row, next try in 40m0s") {
		t.Fatalf("logs = %q", logs)
	}

	m.err = nil
	if n := e.RefreshProjects(ctx, 3); n != 1 {
		t.Fatalf("refreshed %d after the model came back", n)
	}
	if _, ok := e.projectRetries["a"]; ok {
		t.Error("the failures were not cleared by a success")
	}
	if p, _ := e.Store.Project("a"); p.Dirty {
		t.Error("the project is still dirty")
	}
}

// One failing project puts off only itself; the others are still rewritten in the same round.
func TestRefreshProjectsFailureWaitsAlone(t *testing.T) {
	ctx := context.Background()
	m := &failingModel{err: errors.New("ai: no JSON object in reply")}
	e := testEngine(t, m)
	dirtyProject(t, e, "a")
	if n := e.RefreshProjects(ctx, 3); n != 0 {
		t.Fatalf("refreshed %d", n)
	}
	m.err = nil
	dirtyProject(t, e, "b")
	if n := e.RefreshProjects(ctx, 3); n != 1 {
		t.Fatalf("refreshed %d, want only b", n)
	}
	if p, _ := e.Store.Project("a"); !p.Dirty {
		t.Error("a was refreshed before its next try")
	}
	if p, _ := e.Store.Project("b"); p.Dirty {
		t.Error("b waited with a")
	}
}

// A project that is no longer dirty (done by hand, merged, deleted) leaves the failures behind.
func TestRefreshProjectsForgetsProjectsNoLongerDirty(t *testing.T) {
	ctx := context.Background()
	m := &failingModel{err: errors.New("boom")}
	e := testEngine(t, m)
	dirtyProject(t, e, "a")
	e.RefreshProjects(ctx, 3)
	p, _ := e.Store.Project("a")
	p.Dirty = false
	if err := e.Store.PutProject(p); err != nil {
		t.Fatal(err)
	}
	e.RefreshProjects(ctx, 3)
	if len(e.projectRetries) != 0 {
		t.Fatalf("retries = %v", e.projectRetries)
	}
}

func TestRetryWait(t *testing.T) {
	for failures, want := range map[int]time.Duration{0: compressRetry, 1: compressRetry, 2: 2 * compressRetry, 6: 320 * time.Minute, 7: compressRetryMax, 40: compressRetryMax} {
		if got := retryWait(failures); got != want {
			t.Errorf("retryWait(%d) = %v, want %v", failures, got, want)
		}
	}
}
