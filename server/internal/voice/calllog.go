package voice

import (
	"errors"
	"fmt"
	"maps"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// A call's log (#1485, docs/assistant/voice.md "Diagnosing a call"): one line per meaningful event of the call, each
// beginning with the call's id ("call 7:", the calls this extension took since it started), so the lines of calls at
// the same time can be told apart and what happened in one can be told afterwards: how it started (protocol, target,
// model, tools), its upstream sessions, every handover and the user-speech gate's decision on it, every run's states
// and sends to the hub, the cards asked, and how it ended.
//
// Never what was said, the model's replies, a task's text, a card's contents, keys or tokens: ids, states, codes,
// counts and times only, so the log can be shared.

// callLog is what a call's last line counts (its own lock, taken last: nothing else is taken while it is held).
type callLog struct {
	id     int64
	opened time.Time

	mu                 sync.Mutex
	typed              int // lines the user typed
	delegations, calls int
	runs               map[string]string // the call's runs (not observed ones) by id: their last state
	observed           int
	cards              int // cards the model was told to ask
	reconnects         int
	nudges             int    // the model was nudged for an answer it owed (nudge.go)
	reason             string // why the call ended: the first one said
}

// logf writes a line of the call's to the extension's log (none for a conversation a unit test builds without its
// plugin).
func (c *conversation) logf(format string, args ...any) {
	if c.pl == nil || c.pl.logger == nil {
		return
	}
	c.pl.logf("call %d: "+format, append([]any{c.log.id}, args...)...)
}

// count adds to the call's counts.
func (c *conversation) count(f func(l *callLog)) {
	c.log.mu.Lock()
	f(&c.log)
	c.log.mu.Unlock()
}

// ending notes why the call ends, unless that was said already.
func (c *conversation) ending(reason string) {
	c.count(func(l *callLog) {
		if l.reason == "" {
			l.reason = reason
		}
	})
}

// callLines are what the call's last line counts of the conversation's own: the user's lines (captions and typed) and
// the model's that had words, and how often its upstream paused.
type callLines struct{ user, model, pauses int }

// summary is the call's last line, after "conversation closed".
func (c *conversation) summary(n callLines) string {
	l := &c.log
	l.mu.Lock()
	defer l.mu.Unlock()
	states := map[string]int{}
	for _, s := range l.runs {
		states[firstNonEmpty(s, "new")]++
	}
	var runs []string
	for _, s := range slices.Sorted(maps.Keys(states)) {
		runs = append(runs, fmt.Sprintf("%s %d", s, states[s]))
	}
	byState := ""
	if len(runs) > 0 {
		byState = " (" + strings.Join(runs, ", ") + ")"
	}
	took := time.Duration(0)
	if !l.opened.IsZero() {
		took = time.Since(l.opened).Round(time.Second)
	}
	return fmt.Sprintf("after %v (%s): user lines %d (typed %d), model lines %d, delegations %d, function calls %d, runs %d%s, "+
		"observed turns %d, cards asked %d, reconnects %d, pauses %d, nudges %d", took, firstNonEmpty(l.reason, "ended"), n.user,
		l.typed, n.model, l.delegations, l.calls, len(l.runs), byState, l.observed, l.cards, l.reconnects, n.pauses, l.nudges)
}

// errorRunes is the most of the service's own words on an error of a live session that the log keeps.
const errorRunes = 200

// appGone is why the app's socket ended, as readApp read it.
func appGone(err error) string {
	if s := websocket.CloseStatus(err); s != -1 {
		return fmt.Sprintf("the app hung up (%d)", s)
	}
	return "the app's connection dropped"
}

// errCode is the code a failure is known by ("" none): what the app is told, or the service's refusal's.
func errCode(err error) string {
	var ce *codedError
	var ue *upstreamError
	var sr *sessionRefused
	switch {
	case err == nil:
		return ""
	case errors.Is(err, errNoUpstream):
		return codeNoUpstream
	case errors.As(err, &ce) && ce.code != "":
		return ce.code
	case errors.As(err, &ue) && ue.code != "":
		return ue.code
	case errors.As(err, &sr):
		return firstNonEmpty(sr.code, "refused")
	}
	return ""
}

// failure is a failure for the log: its code, and its own words unless they are only what the app is shown.
func failure(err error) string {
	code := errCode(err)
	var ce *codedError
	switch {
	case errors.As(err, &ce):
		return firstNonEmpty(code, "no code")
	case code != "":
		return code + ": " + err.Error()
	}
	return err.Error()
}

// hubOutcome is how a call to the hub that sends went, for the log: ok, its status and code, or no answer and why.
// Never the hub's words: a refusal may quote what was typed (a draft, a dialog).
func hubOutcome(err error) string {
	var he *hubError
	switch {
	case err == nil:
		return "ok"
	case errors.As(err, &he):
		s := fmt.Sprintf("HTTP %d", he.Status)
		if he.Code != "" {
			s += " " + he.Code
		}
		if he.Delivered == "unknown" {
			s += ", delivered unknown"
		}
		return s
	}
	return "no answer (" + transportError(err) + ")"
}

// transportError is a failed request's own error, without the request's URL.
func transportError(err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return err.Error()
}

// logDur is d for the log: to 10 ms past a second, to the millisecond past one, else to the microsecond.
func logDur(d time.Duration) time.Duration {
	switch {
	case d >= time.Second:
		return d.Round(10 * time.Millisecond)
	case d >= time.Millisecond:
		return d.Round(time.Millisecond)
	}
	return d.Round(time.Microsecond)
}

// taskSize is how long a handed-over task is, for the log (never its words).
func taskSize(task string) string {
	if strings.TrimSpace(task) == "" {
		return "no words: the user's last line"
	}
	return fmt.Sprintf("%d characters", runeLen(task))
}

// sessionNo is how the log calls upstream session gen: 1 for the call's first.
func sessionNo(gen int) int { return gen + 1 }

// handoverName is what the log calls a handover: by its delegation, else by the function call it came on.
func handoverName(delegation, call string) string {
	switch {
	case delegation != "":
		return "delegation " + delegation
	case call != "":
		return "call " + call
	}
	return "a handover without an id"
}

// logName is what the log calls the conversation a call is bound to: its kind and ids.
func (b *boundTarget) logName() string {
	if b == nil {
		return "the main conversation"
	}
	return b.Kind + " " + b.MachineID + "/" + b.SessionID
}

// logStart is the call's line once its start is read: what it asked for and what it got (before its upstream connects).
func (c *conversation) logStart(s *startFrame, p *Provider) {
	c.mu.Lock()
	lines, runs := len(c.history), len(c.resume)
	resumed, echoes, modelID, voice := c.resumed, c.gate.echoes, c.modelID, c.voice
	c.mu.Unlock()
	protocol := fmt.Sprintf("protocol %d", c.version)
	if c.asked > c.version {
		protocol += fmt.Sprintf(" (asked %d)", c.asked)
	}
	target := "the main conversation of the moment"
	if c.version >= 2 && s.Target != nil {
		target = s.Target.MachineID + "/" + s.Target.SessionID
	}
	var more []string
	if c.lang != "" {
		more = append(more, "language "+string(c.lang))
	}
	if echoes {
		more = append(more, "no echo cancellation")
	}
	if resumed {
		more = append(more, fmt.Sprintf("picked up again with %d lines and %d runs", lines, runs))
	}
	extra := ""
	if len(more) > 0 {
		extra = ", " + strings.Join(more, ", ")
	}
	c.logf("start: %s, model %s (%s dialect), voice %q, target %s%s", protocol, modelID, p.Dialect, voice, target, extra)
}

// toolsLog is the function tools a session declares, for the log: their names and the instructions' mode, or why none.
func (c *conversation) toolsLog(p *Provider, tools []string) string {
	if len(tools) > 0 {
		return strings.Join(tools, ",") + " (" + c.toolMode(tools) + ")"
	}
	declare, _ := p.toolsMode()
	switch {
	case p.Dialect != DialectLive:
		return "none (the " + p.Dialect + " dialect takes none)"
	case !declare:
		return "none (the provider is set to declare none)"
	case c.toolless.Load():
		return "none (its service refused them)"
	}
	return "none"
}

// runState logs run r going to state (a change only; a fold is logged by adopt) and counts it: the call's last line
// says how its runs ended.
func (c *conversation) runState(r *voiceRun, state string) {
	if r.ID == "" {
		return
	}
	if state != r.State && state != runFolded {
		observed, code := "", ""
		if r.Observed {
			observed = " (observed)"
		}
		if r.Code != "" {
			code = " (" + r.Code + ")"
		}
		c.logf("run %s%s: %s%s", r.ID, observed, state, code)
	}
	if r.Observed {
		return
	}
	c.count(func(l *callLog) {
		if l.runs == nil {
			l.runs = map[string]string{}
		}
		l.runs[r.ID] = state
	})
}
