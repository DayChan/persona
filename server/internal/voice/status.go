package voice

import (
	"context"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A status asked for ("status: …") is answered from the bound conversation's live state, never sent to it, and is no
// write: not held to the user-speech gate (docs/assistant/voice.md "Status queries"). A stop ("stop: …") is a write: behind the gate,
// it interrupts the conversation's turn (the app's "Stop" button) and is told once that worked. Both answer on the
// delegation that asked, as its one commentary.

const (
	statusTimeout = 10 * time.Second
	latestRunes   = 300
)

// answerStatus tells the model how the bound conversation stands, on delegation of upstream session gen (name: what
// the log calls it).
func (c *conversation) answerStatus(name, delegation string, gen int) {
	ctx, cancel := context.WithTimeout(c.ctx, statusTimeout)
	defer cancel()
	hub, t, l := c.pl.hub, c.target, c.language()
	st := prompts.VoiceStatus{Lang: string(l), To: t.to(), Do: "status"}
	sess, err := hub.session(ctx, t.MachineID, t.SessionID)
	switch {
	case err != nil:
		st.Do = "unreachable"
	case sess == nil:
		st.Do = "gone"
	default:
		st.State = sess.State
		switch {
		case atWork(sess):
			st.State = proto.StateWorking
			st.Activity = activityIn(l, sess.Activity) // in the call's language, the category only (#4841)
			if !sess.StateSince.IsZero() {
				st.Elapsed = channel.Duration(l, time.Since(sess.StateSince))
			}
		case waitingOnUser(sess): // listed working while a card waits on its hook is a wait on the user (#5500)
			st.State = proto.StateNeedsYou
		}
		if sess.Work != nil {
			st.Work = clipRunes(oneLine(sess.Work.Line), 200)
		}
		if page, err := hub.lastTurns(ctx, t.MachineID, t.SessionID, 1); err == nil {
			st.Latest = latestWords(page.Items)
			if st.Latest != "" {
				st.Latest = clipRunes(oneLine(speakable(l, st.Latest)), latestRunes)
			}
		}
		if ixs, err := hub.interactions(ctx); err == nil {
			st.Cards = cardsOf(ixs, t)
		}
	}
	if ctx.Err() != nil && c.ctx.Err() != nil {
		return
	}
	how := st.Do // unreachable, gone
	if st.Do == "status" {
		how = firstNonEmpty(st.State, "no state")
	}
	c.logf("%s: status answered: %s, %d cards waiting", name, how, len(st.Cards))
	c.tell(&voiceRun{Delegation: delegation, Gen: gen}, plainWords(prompts.RenderVoiceStatus(st)), true, st.Latest != "" || st.Work != "")
}

// stopTarget interrupts the bound conversation's turn (working, or waiting on the user mid-turn) and tells the model
// how that went (name: what the log calls the delegation that asked).
func (c *conversation) stopTarget(name, delegation string, gen int) {
	ctx, cancel := context.WithTimeout(c.ctx, statusTimeout)
	defer cancel()
	hub, t := c.pl.hub, c.target
	st := prompts.VoiceStatus{Lang: string(c.language()), To: t.to(), Do: "stopped"}
	sess, err := hub.session(ctx, t.MachineID, t.SessionID)
	switch {
	case err != nil:
		st.Do = "stop_failed"
	case sess == nil:
		st.Do = "gone"
	case !working(sess) && !waitingOnUser(sess):
		st.Do = "idle"
	default:
		// Working, or its turn stopped on a card or a dialog: the interrupt (Esc) ends it, as the app's "Stop" does. Noted
		// first: the card asked goes before the hub answers, and the tracker may see that first (asks.go).
		c.runs.stopAsked.Store(time.Now().UnixNano()) // before the stop can be read back (tracker.finish)
		c.stopping(true)
		if err := hub.interrupt(ctx, t.MachineID, t.SessionID); err != nil {
			c.logf("stop %s/%s: %s", t.MachineID, t.SessionID, hubOutcome(err))
			c.runs.stopAsked.Store(0) // the stop did not go through: a later stop from elsewhere is told as usual
			c.stopping(false)
			st.Do, st.Reason = "stop_failed", hubReason(err)
		} else {
			c.runs.stopped()
		}
	}
	if c.ctx.Err() != nil {
		return
	}
	c.logf("%s: stop: %s", name, st.Do)
	c.tell(&voiceRun{Delegation: delegation, Gen: gen}, plainWords(prompts.RenderVoiceStatus(st)), true, false)
}

// activityKind is what sort of step an activity is (transcript.Activity: "reading tasks.go" is reading): never the file,
// the command or the pattern (docs/assistant/voice.md "Status queries": only the category).
func activityKind(activity string) string {
	verb, _, _ := strings.Cut(strings.TrimSpace(activity), " ")
	return verb
}

// latestWords is the conversation's latest words in items (its last exchange): its last text of the assistant's.
func latestWords(items []proto.Item) string {
	for i := len(items) - 1; i >= 0; i-- {
		it := items[i]
		if it.Role == "assistant" && it.Kind == proto.KindText && strings.TrimSpace(it.Text) != "" {
			return it.Text
		}
	}
	return ""
}
