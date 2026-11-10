package assistant

import (
	"cmp"
	"context"
	"hash/fnv"
	"slices"
	"strings"
	"time"
)

// Spot-check reviews (#3128, docs/assistant/todos.md "Spot-check reviews"): with every finished supervised task waiting
// for the main conversation's task_review, the main conversation is the bottleneck once there are many. With
// assistant.review_sample_percent below 100, the done judge also checks each done_when criterion (taskAutoCheck); a
// task whose criteria are all checkable and met and whose work is live is closed by the judge itself (its review is
// the judge's, with the checks), and only a fixed share of those (spotChecked) still comes to the main conversation.
// A task that fails the check waits for task_review as before. At 100 (the default) the judge checks nothing more and
// every finished task waits for task_review.

// reviewerJudge marks a review the done judge made itself (TaskReview.By).
const reviewerJudge = "judge"

// reviewSamplePercent is assistant.review_sample_percent: the share of the finished supervised tasks that passed the
// judge's check whose review still goes to the main conversation; 100 when unset.
func (s *Service) reviewSamplePercent(ctx context.Context) int {
	if p := s.settings(ctx).ReviewSamplePercent; p != nil && *p >= 0 && *p <= 100 {
		return *p
	}
	return 100
}

// taskAutoCheck is the judge's own check of a supervised task's done_when: each criterion (in the ledger's words and
// order) with whether it can be checked from evidence, whether it is met and the evidence, and how far the work went
// out (a delivery state).
type taskAutoCheck struct {
	Checks   []autoCheckItem `json:"checks"`
	Delivery string          `json:"delivery"`
}

type autoCheckItem struct {
	ReviewCheck
	Checkable bool `json:"checkable"`
}

// passed: every criterion is checkable and met with evidence, and the work is live. A task with no criteria never
// passes: there is nothing to check it against.
func (c *taskAutoCheck) passed() bool {
	if c == nil || len(c.Checks) == 0 || c.Delivery != deliveryLive {
		return false
	}
	for _, x := range c.Checks {
		if !x.Checkable || !x.Met || strings.TrimSpace(x.Evidence) == "" {
			return false
		}
	}
	return true
}

// autoCheckOf is the judge's checks matched to criteria: one per criterion in order, the criterion as the ledger has
// it. A check counts only when the judge gave exactly one per criterion and copied each criterion where it belongs;
// otherwise every criterion is left unchecked (not met): a list that is off by one must not pass.
func autoCheckOf(criteria []string, got []autoCheckItem, delivery string) *taskAutoCheck {
	c := &taskAutoCheck{Delivery: delivery}
	aligned := len(got) == len(criteria)
	for i := 0; aligned && i < len(got); i++ {
		aligned = sameCriterion(got[i].Criterion, criteria[i])
	}
	for i, crit := range criteria {
		x := autoCheckItem{ReviewCheck: ReviewCheck{Criterion: crit}}
		if aligned {
			x.Checkable, x.Met, x.Evidence = got[i].Checkable, got[i].Met, clip(oneLine(got[i].Evidence), 300)
		}
		c.Checks = append(c.Checks, x)
	}
	return c
}

// sameCriterion: the judge copied criterion want as got (spacing aside).
func sameCriterion(got, want string) bool {
	return strings.Join(strings.Fields(got), " ") == strings.Join(strings.Fields(want), " ")
}

// judgeMayClose: the judge may close supervised task t now: its first round, never reviewed, never reopened. A rework
// round waits for the review the supervisor was promised (with what it asked beyond done_when), and a task someone
// reopened was reopened for a reason the criteria do not hold.
func judgeMayClose(t Todo, v *Supervision) bool {
	if t.DoneAt != nil || t.ReopenedAt != nil || v == nil || v.state(t) != "" || v.Round > 0 {
		return false
	}
	return !slices.ContainsFunc(v.Reviews, func(r TaskReview) bool { return r.Verdict == reviewAccepted || r.Verdict == reviewCancelled })
}

// spotCheck is spotChecked; a variable for the tests.
var spotCheck = spotChecked

// spotChecked: of the tasks that passed, this stretch of work of task id is one the main conversation still reviews.
// The same task and work always say the same, so a restart or a second judgment does not change it.
func spotChecked(id, work string, pct int) bool {
	if pct >= 100 {
		return true
	}
	if pct <= 0 {
		return false
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(id + "\x00" + work))
	return int(h.Sum32()%100) < pct
}

// judgeClose closes supervised task t, whose work since key passed the judge's check and was not sampled, as the main
// conversation's accepted would: the review is the judge's, with the checks; the task's conclusion card goes to the
// owner. False when the task changed meanwhile (it waits for review instead) or could not be closed.
func (s *Service) judgeClose(ctx context.Context, t Todo, key, reason string, c *taskAutoCheck) bool {
	checks := make([]ReviewCheck, 0, len(c.Checks))
	for _, x := range c.Checks {
		checks = append(checks, x.ReviewCheck)
	}
	note := cmp.Or(clip(oneLine(reason), 80), "判官核对全过、已上线") // i18n-ignore: the review's note, as the supervisor's are
	if _, changed, err := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
		cur, ok := s.todoNow(t.ID) // as it is now: the supervisor may have reviewed it since the judge was asked
		if !ok || !judgeMayClose(cur, v) {
			return false
		}
		v.State = reviewAccepted
		v.Reviews = append(v.Reviews, TaskReview{At: time.Now().UTC(), Verdict: reviewAccepted, By: reviewerJudge, Checks: checks, Note: note})
		if len(v.Reviews) > 20 {
			v.Reviews = v.Reviews[len(v.Reviews)-20:]
		}
		return true
	}); err != nil || !changed {
		if err != nil {
			s.logf("task %s: judge close: %v", t.ID, err)
		}
		return false
	}
	closed, err := s.reviewDoneTodo(ctx, t.ID, "判官核对通过："+note, true) // i18n-ignore: done_reason, as a review's
	if err != nil {
		s.logf("task %s: judge close: %v", t.ID, err)
		// Not closed: the judge's acceptance comes off the ledger again, and the task waits for review as before.
		if _, _, uerr := s.updateSupervision(ctx, t.ID, func(v *Supervision) bool {
			n := len(v.Reviews)
			if v.State != reviewAccepted || n == 0 || v.Reviews[n-1].By != reviewerJudge {
				return false
			}
			v.Reviews, v.State = v.Reviews[:n-1], ""
			return true
		}); uerr != nil {
			s.logf("task %s: judge close: undo: %v", t.ID, uerr)
		}
		return false
	}
	b := &s.supervise
	b.mu.Lock()
	b.init()
	for _, k := range []string{sigReview, sigIdle, sigNeedsYou, sigStalled} {
		delete(b.pending, t.ID+"|"+k)
	}
	b.mu.Unlock()
	if closed {
		s.dropTaskReport(t.ID)
	}
	// The feed and the conclusion card post over the network: not under the completion lock superviseJudged holds.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
		defer cancel()
		_, _ = s.appendFeed(ctx, t.Origin, feedReview, t.ID, reviewFeedBody{Title: t.Title, Verdict: reviewAccepted, Note: note, Checks: checks})
		if cur, ok := s.todoNow(t.ID); ok && closed {
			s.askConclusion(ctx, cur, conclusionInput{Outcome: outcomeDone, Reason: reason, Note: note, Checks: checks})
		}
	}()
	s.logf("task %s: closed by the judge, its check passed (%d criteria, live; work %s)", t.ID, len(checks), key)
	return true
}
