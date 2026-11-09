package assistant

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ---- around every call ----

// gate is what is checked before every call and during every wait: the run ends when the process does, when STOP is
// there, when kb.history no longer enables the source, when the knowledge base is not writable, or when the plan is
// no longer approved, the selection reading more than was approved among the reasons (the approval is taken back).
func (d *historyDriver) gate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, stopped := readHistoryStop(d.data); stopped {
		return halt(false, "stop_file")
	}
	cfg := d.cfg(ctx)
	if !cfg.On() {
		return halt(false, "disabled")
	}
	if _, ok := cfg.Source(d.source); !ok {
		return halt(false, "not_listed")
	}
	e, err := d.eng(ctx)
	if err != nil {
		return halt(false, "kb_unavailable")
	}
	if p, ok, err := loadHistoryPlan(d.data, d.source); err != nil || !ok || p.ApprovedAt == nil {
		return halt(false, "not_approved")
	}
	sel, ret := historySelection(e)
	if sel.Stopgap() { // the document does not read now: the approval is kept, and the next round tries again
		return halt(false, "selection_unavailable")
	}
	if n, err := d.revokeWidened(d.source, sel, ret); err != nil {
		return halt(true, "plan_save_failed", "class", errClass(err))
	} else if n > 0 {
		return halt(false, "plan_changed", "count", strconv.Itoa(n))
	}
	return d.beat(false)
}

// beat renews the lease's heartbeat, at most once a minute unless forced.
func (d *historyDriver) beat(force bool) error {
	if d.lock == nil {
		return nil
	}
	now := d.now()
	if !force && now.Sub(d.beatAt) < time.Minute {
		return nil
	}
	d.beatAt = now
	return d.lock.beat(now, d.source)
}

// wait waits dur in steps, going through the gate at each: a stop, or the import turned off, ends it early. A wait
// with a state (a rest, a pause, a quiet period) is kept in status.json, so a restart waits out what is left of it.
func (d *historyDriver) wait(ctx context.Context, dur time.Duration, state string, why historyReason) error {
	if dur <= 0 {
		return d.gate(ctx)
	}
	end := d.now().Add(dur)
	if state != "" {
		d.lastOK = false
		d.pacer.notBefore, d.pacer.waiting = end, state
		d.setStatus(d.source, func(st *historySourceStatus) {
			st.State = state
			st.Until = end.UTC()
			if state == "paused" && why.Code != "" {
				st.LastPause = &historyEvent{At: d.now().UTC(), historyReason: why}
			}
			st.Pace = d.paceState()
		})
	}
	for {
		if err := d.gate(ctx); err != nil {
			return err
		}
		left := end.Sub(d.now())
		if left <= 0 {
			break
		}
		if err := d.sleep(ctx, min(left, historyWaitStep)); err != nil {
			return err
		}
	}
	if state != "" {
		d.pacer.notBefore, d.pacer.waiting = time.Time{}, ""
		d.setStatus(d.source, func(st *historySourceStatus) { st.State = "running"; st.Until = time.Time{}; st.Pace = d.paceState() })
	}
	return nil
}

// paced waits what the pace says before the next call: a quiet period, a rest that is due, then the interval.
func (d *historyDriver) paced(ctx context.Context, image bool) error {
	if end := d.pacer.quietUntil(d.now()); !end.IsZero() {
		d.log(historyLogLine{Event: "rest", historyReason: reason("quiet")})
		if err := d.wait(ctx, end.Sub(d.now()), "quiet", historyReason{}); err != nil {
			return err
		}
	}
	if rest, why := d.pacer.rest(d.now()); rest > 0 {
		d.log(historyLogLine{Event: "rest", historyReason: reason(why, "minutes", strconv.Itoa(int(rest.Minutes())))})
		if err := d.wait(ctx, rest, "resting", historyReason{}); err != nil {
			return err
		}
	}
	return d.wait(ctx, d.pacer.interval(image, d.now()), "", historyReason{})
}

// historyCall is what a call came to: the call's own error (nil: an answer), and what its duration asks for after
// the answer is handled (a rest before the next call, or a stop).
type historyCall struct {
	err    error
	rest   time.Duration
	stop   *historyHalt
	method string
	chat   string // its hash
	prevOK bool   // the call before it went through, with only the interval between (lastOK)
}

// classify is what the call's error means. A source that answers not ready right after a call of ours went through
// is taken as made unready by it (docs/assistant/knowledge.md "Stops"): that stops, where not ready otherwise
// pauses.
func (c historyCall) classify() (historyOutcome, string) {
	o, code := classify(c.err)
	if code == "not_ready" && c.prevOK {
		return historyStopIt, code
	}
	return o, code
}

// stopWhy is the reason a call's stop-class error stops the import for.
func (c historyCall) stopWhy(code string) historyReason {
	r := reason("call_unknown_code", "code", code, "method", c.method, "chat", c.chat)
	switch {
	case code == "not_ready":
		r.Code = "not_ready_after_call"
	case historyTimeoutCode(code):
		r.Code = "call_timeout"
	case code == "connection_closed":
		r.Code = "call_connection_closed"
	case code == "error":
		r.Code = "call_no_code"
	}
	return r
}

// call makes one call of method for chat (its id, hashed in the log) after the pace's wait: one in flight, never
// cancelled by a stop (its context only ends on its own timeout), logged, timed and judged. f returns how many items
// the answer had, its result and its pace for the log. An error from call itself means no call was made: the gate or
// the pace's wait ended the run.
func (d *historyDriver) call(ctx context.Context, method, filing, chat string, image bool, f func(context.Context) (n int, result, pace string, err error)) (historyCall, error) {
	if err := d.paced(ctx, image); err != nil {
		return historyCall{}, err
	}
	d.calls.Lock()
	if !d.connected() {
		d.calls.Unlock()
		return historyCall{}, halt(false, "not_connected")
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.timeout)
	start := d.now()
	if d.pacer.slowPending { // kb_history_resume with slow: the two slow hours start with the first call
		d.pacer.slowPending, d.pacer.slowUntil = false, start.Add(historySlowStart)
		d.pacer.mult = max(d.pacer.mult, d.pacer.maxMult())
		d.setStatus(d.source, func(st *historySourceStatus) { st.SlowPending, st.SlowUntil = false, d.pacer.slowUntil.UTC() })
	}
	n, result, pace, err := f(cctx)
	took := d.now().Sub(start)
	if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}
	cancel()
	d.calls.Unlock()
	if err != nil {
		result = historyErrCode(err)
	}
	hash := historyHash(d.key, filing, chat)
	line := historyLogLine{Method: method, Chat: hash, MS: took.Milliseconds(), Result: result, N: n, Pace: pace}
	d.log(line)
	line.At, line.Source = d.now().UTC(), d.source
	out := historyCall{err: err, method: method, chat: hash, prevOK: d.lastOK}
	d.lastOK = err == nil && pace == ""
	if d.lastOK {
		d.pausedSince = time.Time{}
	}
	v := d.pacer.observe(method, took, d.now())
	out.rest = v.rest
	if v.stop.Code != "" {
		out.stop = haltWhy(true, v.stop)
	}
	d.setStatus(d.source, func(st *historySourceStatus) {
		st.LastCall = &line
		st.Calls++
		if v.note.Code != "" {
			st.LastSlow = &historyEvent{At: d.now().UTC(), historyReason: v.note}
		}
		st.Pace = d.paceState()
	})
	if v.note.Code != "" {
		d.log(historyLogLine{Event: "slow", historyReason: v.note})
	}
	return out, nil
}

// after does what a call's duration asked for, once its answer is handled: stop, or rest.
func (d *historyDriver) after(ctx context.Context, c historyCall) error {
	if c.stop != nil {
		return c.stop
	}
	if c.rest > 0 {
		d.log(historyLogLine{Event: "rest", historyReason: reason("slow_rest", "minutes", strconv.Itoa(int(c.rest.Minutes())))})
		return d.wait(ctx, c.rest, "resting", historyReason{})
	}
	return nil
}

// pause waits after a pace hint or a transient error, then the same read goes again; paused longer than two hours
// in a row (nothing went through since), it stops instead.
func (d *historyDriver) pause(ctx context.Context, dur time.Duration, why historyReason) error {
	now := d.now()
	if d.pausedSince.IsZero() {
		d.pausedSince = now
	}
	if now.Add(dur).Sub(d.pausedSince) > historyPauseMost {
		return halt(true, "pause_too_long", "hours", strconv.Itoa(int(historyPauseMost.Hours())), "code", why.Args["code"])
	}
	d.log(historyLogLine{Event: "pause", historyReason: why})
	return d.wait(ctx, dur, "paused", why)
}

// pauseFor is the reason of a pause for code: a source's retry_after (with how long it asked) or a transient code.
func pauseFor(code string) historyReason {
	if ms, ok := strings.CutPrefix(code, "retry_after="); ok {
		n, _ := strconv.ParseInt(ms, 10, 64)
		return reason("source_pace", "code", "retry_after", "seconds", strconv.FormatInt(n/1000, 10))
	}
	return reason("transient", "code", code)
}

// ---- errors ----

// What an answer or an error means for the import.
type historyOutcome int

const (
	historyNone       historyOutcome = iota // (the zero value: nothing to act on)
	historyPauseIt                          // wait, then read the same again
	historyStopIt                           // stop the whole import for a person
	historyChatError                        // this chat cannot be read as asked: error, permanent
	historyUnreadable                       // this chat cannot be read any more: skipped, not a failure
)

// Codes a source answers that pass by themselves (the import waits and reads the same again).
var historyTransient = map[string]bool{"busy": true, "not_ready": true, "rate_limited": true, "too_many_requests": true}

// Codes that say this one chat cannot be read: gone (unreadable, as a page with unreadable says) or refused.
var historyGone = map[string]bool{"not_found": true, "gone": true, "conversation_not_found": true}
var historyRefused = map[string]bool{"forbidden": true, "permission_denied": true, "no_permission": true, "not_member": true,
	"conversation_not_member": true}

// historyErrCode is an error's code for the log and the status: never its message, which may name a chat.
func historyErrCode(err error) string {
	var re *rpc.Error
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case isHistoryQueryErr(err):
		return "query"
	case errors.As(err, &re) && re.Code != "":
		return normCode(re.Code)
	case errors.Is(err, rpc.ErrClosed):
		return "connection_closed"
	}
	return "error"
}

func normCode(c string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(c)), "-", "_")
}

// errClass is a short word for an error of the import's own (a file), for the log.
func errClass(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	}
	return "error"
}

func isHistoryQueryErr(err error) bool {
	return errors.Is(err, connectorwire.ErrQuery) || strings.HasPrefix(err.Error(), connectorwire.ErrQuery.Error()+":")
}

// classify says what err means, with its code. A timeout, a closed connection (the result is unknown, as for a
// timeout) and any code it does not know stop the import: a person looks, nothing is retried by itself.
func classify(err error) (historyOutcome, string) {
	code := historyErrCode(err)
	switch {
	case code == "query":
		return historyChatError, code
	case historyTimeoutCode(code) || code == "connection_closed":
		return historyStopIt, code
	case historyTransient[code]:
		return historyPauseIt, code
	case historyGone[code]:
		return historyUnreadable, code
	case historyRefused[code]:
		return historyChatError, code
	}
	return historyStopIt, code
}

func historyTimeoutCode(code string) bool {
	return strings.Contains(code, "timeout") || strings.Contains(code, "timed_out")
}
