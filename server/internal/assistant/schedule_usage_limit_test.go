package assistant

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// A scheduled run whose conversation a subscription's usage limit stopped (docs/assistant/agent-work.md "Usage
// limits"): failed and pushed as ever, retried once the limit reset rather than after runRetryDelay (when it would be
// refused again), not at all when its CLI goes on by itself.

// limitRun runs task p in conversation s1 on server, its turn ending on Claude Code's usage-limit line, the session
// then as end makes it. It returns once the run is recorded failed.
func limitRun(t *testing.T, n *fakeHub, h *Service, end func(*proto.Session)) {
	t.Helper()
	h.turnPoll = 200 * time.Millisecond
	n.items = []proto.Item{{Role: "assistant", Kind: "text", Text: limitText}}
	setTasks(h, Task{ID: "p", Title: "值守", Prompt: "go", Repeat: repeatInterval, EveryMinutes: 30, At: time.Now().Add(-time.Minute), Enabled: true,
		MachineID: "server", CreatedBy: "assistant"})
	n.setSessions("server", proto.Session{SessionID: "s1", Status: proto.StatusWorking, State: proto.StateWorking, Assistant: true,
		Cwd: "/home/me/.persona/assistant"})
	if _, err := runNow(t, h, "p"); err != nil {
		t.Fatal(err)
	}
	go func() {
		time.Sleep(3 * h.turnPoll / 2)
		n.mu.Lock()
		for i := range n.sessions["server"] {
			c := &n.sessions["server"][i]
			c.Status, c.State, c.LastText = proto.StatusDone, proto.StateIdle, limitText
			if end != nil {
				end(c)
			}
		}
		n.mu.Unlock()
	}()
	waitFor(t, "the failed run", func() bool {
		sc := h.loadSchedule(context.Background())
		sc.mu.Lock()
		defer sc.mu.Unlock()
		return slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool {
			return r.RefID == "p" && r.Status == runFailed && strings.Contains(r.Error, "You've hit your")
		})
	})
	if p := n.pushedBy(time.Now().Add(2 * time.Second)); len(p) == 0 || p[0].Title != "定时任务失败：值守" {
		t.Fatalf("the failure is pushed: %+v", p)
	}
}

func retried(h *Service) bool {
	sc := h.loadSchedule(context.Background())
	sc.mu.Lock()
	defer sc.mu.Unlock()
	return slices.ContainsFunc(sc.runs, func(r ScheduleRun) bool { return r.RefID == "p" && r.Retried })
}

// The retry waits for the reset the conversation's words give, not runRetryDelay.
func TestUsageLimitScheduledRunIsRetriedAfterTheReset(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = time.Hour // an ordinary failure's retry: not within this test
	var mu sync.Mutex
	var resets []time.Time
	h.sched.limitWait = func(reset, now time.Time) time.Duration {
		mu.Lock()
		resets = append(resets, reset)
		mu.Unlock()
		return 50 * time.Millisecond
	}
	limitRun(t, n, h, nil)
	waitFor(t, "the retry", func() bool { return n.sent() >= 2 && retried(h) })
	want, _ := transcript.UsageLimitReset(limitText, time.Now())
	mu.Lock()
	defer mu.Unlock()
	if len(resets) != 1 || !resets[0].Equal(want) {
		t.Fatalf("waited for %v, want %v", resets, want)
	}
}

// A conversation that goes on by itself once the limit resets is not retried.
func TestUsageLimitScheduledRunThatResumesByItselfIsNotRetried(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = 50 * time.Millisecond
	h.sched.limitWait = func(time.Time, time.Time) time.Duration { return 50 * time.Millisecond }
	reset := time.Now().Add(time.Hour)
	limitRun(t, n, h, func(c *proto.Session) {
		c.TurnError, c.TurnErrorKind, c.TurnErrorResetsAt, c.TurnErrorResumes = limitText, proto.TurnErrorUsageLimit, &reset, true
	})
	time.Sleep(500 * time.Millisecond)
	if n.sent() != 1 || retried(h) {
		t.Fatalf("retried: %d sends", n.sent())
	}
}

// Without a stand-in, the retry goes a minute or so after the reset, and from now when the reset is already past.
func TestLimitRetryWait(t *testing.T) {
	h := newStorelessService(t, newFakeHub())
	now := time.Now()
	for _, reset := range []time.Time{now.Add(2 * time.Hour), now.Add(-time.Hour)} {
		d := h.limitRetryWait(reset, now)
		from := max(reset.Sub(now), 0)
		if d < from+limitContinueAfter || d >= from+limitContinueAfter+limitContinueSpread {
			t.Errorf("reset %v from now: waits %v", reset.Sub(now), d)
		}
	}
}

// The wait for the reset is long: a conversation that went on meanwhile (the user said "go on" after the reset) is not
// retried, so what the run does is not done twice.
func TestUsageLimitScheduledRunThatWentOnIsNotRetried(t *testing.T) {
	n := newFakeHub().withMachine()
	h := newStorelessService(t, n)
	h.runRetry = time.Hour
	stopped := time.Now().Add(-time.Minute)
	h.sched.limitWait = func(time.Time, time.Time) time.Duration {
		n.mu.Lock()
		for i := range n.sessions["server"] {
			n.sessions["server"][i].UpdatedAt = time.Now() // a later turn ended meanwhile
			n.sessions["server"][i].LastText = "发好了"
		}
		n.mu.Unlock()
		return 50 * time.Millisecond
	}
	limitRun(t, n, h, func(c *proto.Session) { c.UpdatedAt = stopped })
	time.Sleep(500 * time.Millisecond)
	if n.sent() != 1 || retried(h) {
		t.Fatalf("retried: %d sends", n.sent())
	}
}
