package assistant

import (
	"math/rand/v2"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Subscription usage limits (docs/assistant/agent-work.md "Usage limits", #1710): a turn a subscription's 5-hour or
// weekly limit refused is no failure to report and nothing for the user to fix. It waits for the reset; the CLI goes
// on by itself when it said so (typing into it would cancel that), otherwise the manager continues it a minute or so
// after the reset (agents_work_act.go). Only a subscription's limit has a reset time to wait for
// (proto.Session.TurnErrorResetsAt): a gateway's or an API key's 429 is handled as before.

const (
	// limitContinueAfter / limitContinueSpread: the manager continues a turn its usage limit stopped this long after
	// the reset, spread over limitContinueSpread so that the agents on one account do not all start in the same second.
	limitContinueAfter  = time.Minute
	limitContinueSpread = 30 * time.Second
	// maxLimitContinues: how many times the manager continues after a usage limit with no turn that did any work in
	// between (a reset read too early: each continuation is refused again at once).
	maxLimitContinues = 3
	// limitResumeGrace: how long past its reset a usage-limit wait still counts as one: the CLI resumes, or the manager
	// continues, within it.
	limitResumeGrace = 10 * time.Minute
)

// decorateLimit completes what the session list says of a usage limit (sess decorated with its turn error): Claude
// Code's own line for one counts as one when the node did not say (an older node, an older Claude Code), its reset
// read off its words when they name a time zone. A kind the node gave stands: it read the record, and a gateway's 429
// without quotaLimits is not a subscription's limit. Without a turn error, or for another kind, nothing of a limit is left.
func decorateLimit(sess *proto.Session) {
	if sess.TurnErrorKind == "" && transcript.UsageLimitText(sess.TurnError) {
		sess.TurnErrorKind = proto.TurnErrorUsageLimit
	}
	if sess.TurnError == "" || sess.TurnErrorKind != proto.TurnErrorUsageLimit {
		sess.TurnErrorResetsAt, sess.TurnErrorResumes, sess.TurnErrorAfterWork = nil, false, false
		return
	}
	if sess.TurnErrorResetsAt == nil {
		if t, ok := transcript.UsageLimitReset(sess.TurnError, sess.UpdatedAt); ok {
			sess.TurnErrorResetsAt = &t
		}
	}
}

// usageLimitReset is when the usage limit that ended sess's turn resets: sess (decorated) stopped on a subscription's
// usage limit whose reset is known.
func usageLimitReset(sess proto.Session) (time.Time, bool) {
	if sess.TurnErrorKind != proto.TurnErrorUsageLimit || sess.TurnErrorResetsAt == nil || sess.TurnError == "" {
		return time.Time{}, false
	}
	return *sess.TurnErrorResetsAt, true
}

// usageLimitWaiting: sess stopped on a usage limit and waits it out — its reset is known and not long past: its task
// is not interrupted for the user to continue (a "Continue" before the reset is refused at once).
func usageLimitWaiting(sess proto.Session, now time.Time) bool {
	reset, ok := usageLimitReset(sess)
	return ok && now.Before(reset.Add(limitResumeGrace))
}

// limitTakenUp: sess waits out its usage limit (usageLimitWaiting) and something goes on with it after the reset:
// its CLI said it would (TurnErrorResumes), or the manager is to continue it or has asked the user (its work's wake or
// card); a CLI that said so goes on even when the reset is not known. Its turn is not told as finished or interrupted
// meanwhile (no push, no task receipt): it goes on. One nothing takes up (the switches off, a CLI that does not
// resume) is told as it ends, as a usage limit the user goes on from.
func limitTakenUp(sess proto.Session, now time.Time) bool {
	if sess.TurnError != "" && sess.TurnErrorKind == proto.TurnErrorUsageLimit && sess.TurnErrorResumes {
		reset, ok := usageLimitReset(sess)
		return !ok || now.Before(reset.Add(limitResumeGrace)) // its CLI goes on, whenever the reset is
	}
	if !usageLimitWaiting(sess, now) {
		return false
	}
	w := sess.Work
	return w != nil && w.ResetsAt != nil && (w.WakeAt != nil || w.InteractionID != "")
}

// limitLookAgain is when a usage-limit wait sess's CLI or the manager takes up is looked at again for its task's
// receipt: once it is over (limitResumeGrace past the reset), or, with no reset known, a five-hour window later.
func limitLookAgain(sess proto.Session, now time.Time) time.Time {
	if reset, ok := usageLimitReset(sess); ok {
		return reset.Add(limitResumeGrace)
	}
	return now.Add(5*time.Hour + limitResumeGrace)
}

// limitContinueAt is when the manager continues work a usage limit stopped, the limit resetting at reset: about a
// minute after it (after now, when the reset is already past), spread a little.
func limitContinueAt(reset, now time.Time) time.Time {
	at := reset
	if at.Before(now) {
		at = now
	}
	return at.Add(limitContinueAfter + rand.N(limitContinueSpread)).UTC()
}
