package voice

import (
	"encoding/json"
	"github.com/coder/websocket"
	"strings"
	"time"
)

// readApp pumps the app's frames until it hangs up.
func (c *conversation) readApp() {
	for {
		typ, b, err := c.app.Read(c.ctx)
		if err != nil {
			if c.ctx.Err() != nil {
				c.ending("the call's socket to the app failed")
			} else {
				c.ending(appGone(err))
			}
			return
		}
		if typ == websocket.MessageBinary {
			ms, now := meanSquare(b), time.Now()
			c.mu.Lock()
			up, ready, w := c.up, c.ready, c.w
			voiced := c.pace.heard(now, ms)
			held := c.held(b, voiced) // paused or waking: kept for the next session (suspend.go)
			c.heardOwn(now, voiced, len(b), held, ready)
			if !held {
				c.cutIn(now, voiced, len(b))
			}
			c.mu.Unlock()
			if !held && ready && len(b) > 0 {
				_ = c.writeUp(up, w.audio(b)) // a stuck session: its reader sees the close and lost runs
			}
			continue
		}
		var msg struct {
			Type       string `json:"type"`
			Text       string `json:"text"`
			ID         string `json:"id"`
			ClearDraft string `json:"clear_draft"`
		}
		if json.Unmarshal(b, &msg) != nil {
			continue
		}
		switch msg.Type {
		case "interrupt":
			c.mu.Lock()
			c.dropAudio, c.lastAudio, c.cutAt = true, time.Now(), time.Time{}
			c.pace.playEnd = c.lastAudio // the app drops what it had not played
			c.closeAsst()
			c.mu.Unlock()
			c.sendApp(map[string]any{"type": "clear"})
		case "text":
			text := strings.TrimSpace(msg.Text)
			if text == "" {
				continue
			}
			c.count(func(l *callLog) { l.typed++ })
			c.mu.Lock()
			c.closeUser()
			c.userText(text, true, true, time.Now())
			c.gate.user = time.Now()
			c.gate.typed = c.gate.user
			w := c.w
			held := c.holdText(text) // paused or waking: it wakes the upstream, and goes to the next session (suspend.go)
			if !held && c.ready && c.up != nil {
				c.owe(c.gate.user, owedTyped, false) // the model owes it an answer (nudge.go); a held one, once it is sent
			}
			c.mu.Unlock()
			if !held && !c.sendUp(-1, w.text(text)) {
				c.fail("语音会话还没连上，这句没发出去", false)
			}
		case "resend":
			// The user tapped "Resend" / "Clear and send" under a run a 409 refused (protocol 2): their own act, not the model's,
			// so not held to the user-speech gate.
			if c.version >= 2 && msg.ID != "" {
				c.runs.resend(msg.ID, msg.ClearDraft)
			}
		}
	}
}

// heardOwn takes a frame of the app's audio, n bytes long, voiced or not, arrived at now (mu held): the user's own
// voice for spokeVoice (ownVoice) is the user speaking for the user-speech gate, once it reaches the model. Sent as it
// comes when the upstream is up (ready); kept while it is paused or waking (held), it counts once it reaches the woken
// session (flushRing), as a line typed meanwhile does; dropped (no session: reconnecting), it does not count.
func (c *conversation) heardOwn(now time.Time, voiced bool, n int, held, ready bool) {
	if !c.own.add(now, voiced, c.frameLen(n), c.pace.playEnd) {
		return
	}
	switch {
	case held:
		c.sus.spoke = true
	case ready:
		c.gate.voice = now
	}
}

// frameLen is how long n bytes of the app's audio play (0 before its rate is known).
func (c *conversation) frameLen(n int) time.Duration {
	if c.p == nil {
		return 0
	}
	rate := c.p.inputRate()
	if rate <= 0 {
		return 0
	}
	return time.Duration(n/2) * time.Second / time.Duration(rate)
}

// cutIn takes a frame of the app's audio, n bytes long, voiced or not (mu held): the user talking over the model's
// speech for bargeMin drops what the app has not played of it (gate.go "Barging in"). Protocol 2 only, and only on
// an upstream that does not say when the model is cut in on (signals): there clear follows its word. Protocol 1 does
// as it always did (a clear when the upstream says so, or the app asks).
func (c *conversation) cutIn(now time.Time, voiced bool, n int) {
	if c.version < 2 || c.p == nil {
		return
	}
	rate := c.p.inputRate()
	if rate <= 0 {
		return
	}
	talked := c.barge.add(voiced, time.Duration(n/2)*time.Second/time.Duration(rate))
	if talked < bargeMin || c.signals || c.dropAudio || !now.Before(c.pace.playEnd) {
		return
	}
	c.logf("the user cut in on the model (%v of voice): its speech is dropped", talked.Round(10*time.Millisecond))
	c.barge = barge{}
	c.dropAudio, c.lastAudio, c.cutAt = true, now, now
	c.pace.playEnd = now // the app drops what it had not played
	c.closeAsst()
	c.sendApp(map[string]any{"type": "clear"})
}

// replyAfterCut: after the energy rule cut the model off (cutAt), audio coming now is its next reply, not the rest of the
// one it dropped (mu held): the user has been quiet replyAfterVoice, and what came before has paused for cutGap (gate.go
// "Barging in"). The reply to a short cut-in comes before the 1.5 s pause that ends a drop otherwise.
func (c *conversation) replyAfterCut(now time.Time) bool {
	return !c.cutAt.IsZero() && now.Sub(c.pace.voice) >= replyAfterVoice && now.Sub(c.lastAudio) >= cutGap
}
