package voice

import (
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Nudging a silent model (#2297, docs/assistant/voice.md 4.5 rule 9). Now and then the realtime model owes the user an
// answer and says nothing: the user finished a line or typed one, or a result was handed to it (a tool's, a
// delegation's, a refusal, a status), and no caption, audio or function call of the model's follows. The call then sits
// in silence until the user speaks again or hangs up. So an answer owed for nudgeAfter, with the user quiet for
// nudgeQuiet, gets one short say (voice/nudge): the measured service's model always speaks after a say
// (docs/lessons/voice.md "Recommended constants"). At most one per owed answer; the next one owed arms it again.
//
// An answer is owed from:
//   - the user's line, once its caption is final (they stopped, and nothing more of it came): not when the model began
//     to answer, or spoke on over it, after they stopped saying it. The measured service sends the caption 1.2–1.4 s
//     after they stop, often after the model's answer began and sometimes after all its audio is in (it comes 9–10
//     times faster than it plays), so the caption's own arrival says nothing of whether it was answered;
//   - a line the user typed, as it reaches the session (one typed while paused: as the woken session takes it);
//   - what the extension hands the model (sendNow): anything but a heartbeat and the nudge itself.
//
// It is answered by any audio of the model's that goes to the app (not the rest of an answer cut off, which is dropped),
// a caption with words, a delegation or a function call. It is counted from when the model could have spoken: not
// before its speech (by its computed play end) has played out, nor before its session came up.
//
// No nudge while the upstream is paused, waking or reconnecting, or the call is over; while a card is asked or about to
// be (the model waits for the user's answer); while something waits to be said (it goes, and is owed in its turn); when
// the app records without echo cancellation (the service captions the model's own speech as the user's, #5789, so the
// user's lines and their quiet cannot be told); and, for the user's words, while a run of the call's is worked on
// (sending, sent, working, held, blocked) or the bound conversation works: the user waits for that, and heartbeats tell
// them it goes on. A result is nudged for even then: it is what they wait for.

var (
	// nudgeAfter is how long an answer is owed before the model is nudged: past the 0.9–1.3 s the measured model takes
	// to begin an answer (docs/lessons/voice.md "Timeline"), and a reply's 4–5 s, with a wide margin; well short of
	// heartbeatAfter.
	nudgeAfter = 15 * time.Second
	// nudgeQuiet is how long the user has been quiet (no caption growing, no typed line, no voice in the app's audio)
	// before the model is nudged: a user who is still talking is not waiting.
	nudgeQuiet = 4 * time.Second
)

// What an answer is owed for, as the log says it.
const (
	owedCaption    = "the user's caption"
	owedTyped      = "a typed line"
	owedToolResult = "a tool result"
	owedResult     = "a result"
)

// owedAnswer is an answer the model owes the user (under conversation.mu): none while at is zero.
type owedAnswer struct {
	at    time.Time // it became owed
	cause string    // owedCaption…, for the log
	// result: owed for something the extension handed the model, not for the user's words.
	result bool
	nudged bool // the one nudge for it went
}

// owe notes an answer owed from now on for cause (mu held): a new one, its nudge armed again.
func (c *conversation) owe(now time.Time, cause string, result bool) {
	c.owed = owedAnswer{at: now, cause: cause, result: result}
}

// modelActed: the model spoke, wrote a caption, handed something over or called a function at now (mu held): what it
// owed is answered.
func (c *conversation) modelActed(now time.Time) {
	c.modelAt = now
	c.owed = owedAnswer{}
}

// userLineDone: the user's line, of what they stopped saying at at, is final (mu held). An answer is owed for it unless
// the model began one after they stopped, or was still speaking then (it spoke on over them: a backchannel, or what it
// said is the answer). A typed line is owed as it is sent (owedTyped), whatever the model was doing.
func (c *conversation) userLineDone(at time.Time) {
	if at.IsZero() || latest(c.modelAt, c.pace.playEnd).After(at) {
		return
	}
	c.owe(time.Now(), owedCaption, false)
}

// handedOver notes s as owed an answer as it is sent (mu held): what the extension says to the model by itself, but not
// a heartbeat (the model may let one go) or the nudge itself; only to a session that is up (one dropping takes it with
// it, lost).
func (c *conversation) handedOver(s *sentWords, now time.Time) {
	if s.nudge || s.prio == prioHeartbeat || !c.ready || c.up == nil {
		return
	}
	cause := owedResult
	if s.onCall {
		cause = owedToolResult
	}
	c.owe(now, cause, true)
}

// worked: a run of the call's is worked on (not over, and not waiting on the user), or the bound conversation works as
// the tracker last looked (mu held).
func (c *conversation) worked() bool {
	return c.sus.works || slices.ContainsFunc(c.open, func(o openTask) bool { return o.state != runNeedsYou })
}

// nudgeDue: the model owes the user an answer and is to be nudged now (mu held; docs above).
func (c *conversation) nudgeDue(now time.Time) bool {
	o := c.owed
	switch {
	case o.at.IsZero() || o.nudged:
		return false
	case c.closed || !c.ready || c.up == nil || c.sus.state != "":
		return false // no session to nudge (reconnecting, paused, waking), or the call is over
	case c.gate.echoes || c.askCur != "" || len(c.queue) > 0:
		return false
	case !o.result && c.worked():
		return false
	}
	if now.Sub(latest(o.at, c.pace.playEnd, c.connectedAt)) < nudgeAfter {
		return false
	}
	return now.Sub(latest(c.gate.user, c.userAt, c.pace.voice)) >= nudgeQuiet
}

// maybeNudge nudges the model once when it owes the user an answer and has said nothing (nudgeDue).
func (c *conversation) maybeNudge(now time.Time) {
	c.mu.Lock()
	if !c.nudgeDue(now) {
		c.mu.Unlock()
		return
	}
	c.owed.nudged = true
	o := c.owed
	c.mu.Unlock()
	c.count(func(l *callLog) { l.nudges++ })
	c.logf("nudge: answer owed for %v after %s", logDur(now.Sub(o.at)), o.cause)
	cause := "user"
	if o.result {
		cause = "result"
	}
	text := prompts.RenderVoiceNudge(prompts.VoiceNudge{Lang: string(c.language()), Cause: cause})
	c.sendNow(&sentWords{words: plainWords(text), nudge: true})
}
