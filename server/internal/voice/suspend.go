package voice

import (
	"context"
	"slices"
	"time"

	"github.com/coder/websocket"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Pausing the upstream (docs/assistant/voice.md 4.9 "Suspension", protocol 2 only). A realtime session is billed by the second, silence
// included (docs/lessons/voice.md "Other behavior": a 15-minute session the model hardly spoke in was billed 948 s), and an
// agent's turn often takes minutes. So while the bound conversation works on something (or a run of the call's waits
// on its background work or on something outside: what it brings back is still to come), nobody has spoken for
// pauseAfter (the user by their voice, captions or typed lines; the model by its speech as it plays, its handovers;
// the extension by what it told the model), nothing waits to be said, no card is asked and no handover waits for its
// call, the upstream session is closed: the app's socket and the runs stay, the app is told link{paused}, and no
// heartbeat is said while it lasts (it would only wake the upstream to say that the work goes on).
//
// It wakes as soon as there is something to say (a result, a card, a refusal: whatever deliver queues), when the user
// types a line, or when the app's audio has had the user's voice in it for wakeVoice (by the audio's own length, gaps
// between words up to bargeGap; their voice is what pacing.heard says it is, over voiceRMS and over the noise floor, so
// the zero frames a muted app sends never wake it). The app is told link{waking}, a new session is opened as after a
// drop (connect: the bound conversation's snapshot taken again while the socket connects, what was said, the runs
// still waited on) and link{live} once it is up. Meanwhile the app's audio of the last ringSpan is kept and sent to
// the new session first, so what the user says as it wakes (about 1.1–1.6 s on the measured service, lessons
// "Recommended constants") is not lost; lines typed meanwhile go after it. Nothing is said before that has gone out.
//
// The old session's delegations went with it: what comes back for them is said (its words carry the task). Pausing
// and waking are not drops: they do not count toward maxReconnects (a session paused after pauseAfter held
// stableAfter, which starts that count over); a wake is tried wakeMost times in a row before the call ends
// (upstream_failed).

var (
	// pauseAfter is how long the call is quiet while its conversation works before the upstream is closed
	// (docs/lessons/voice.md "Recommended constants": 60 s).
	pauseAfter = time.Minute
	// wakeVoice is how long the user's voice wakes a paused upstream (docs/assistant/voice.md 4.9 "Suspension": 200 ms).
	wakeVoice = 200 * time.Millisecond
	// ringSpan is how much of the app's audio is kept while paused and waking, for the new session.
	ringSpan = 3 * time.Second
	// wakeBackoff is the wait before the next try of a wake that failed (times the tries so far).
	wakeBackoff = time.Second
)

// wakeMost is how many tries in a row a wake gets before the call ends.
const wakeMost = 3

// The link frame's states (docs/assistant/voice.md "Extension → App").
const (
	linkLive         = "live"
	linkPaused       = "paused"
	linkWaking       = "waking"
	linkReconnecting = "reconnecting"
)

// suspension is a call's pausing of its upstream (under conversation.mu).
type suspension struct {
	state  string    // "" live (or not paused: before ready, reconnecting); linkPaused; linkWaking until the ring is out
	works  bool      // the bound conversation works on something the call waits for (the tracker's last look)
	said   time.Time // the extension last told the model something (sendNow)
	at     time.Time // the upstream was paused, or began to wake
	voice  barge     // the user's voice in the app's audio while paused, toward wakeVoice
	ring   ring      // the app's audio while paused and waking, the last ringSpan
	texts  []string  // lines the user typed while paused and waking: sent once the ring is out
	spoke  bool      // the ring has the user's own voice in it (heardOwn): it counts once it reaches the session
	failed int       // tries of a wake that failed in a row
	flush  bool      // a flushRing sends what is kept: one at a time, so it goes out in order
	pauses int       // how often the call paused (the log)
}

// ring is the app's audio of the last span bytes, oldest first.
type ring struct {
	frames [][]byte
	size   int
}

// add keeps frame b, and lets the oldest go past most bytes.
func (r *ring) add(b []byte, most int) {
	if len(b) == 0 {
		return
	}
	r.frames = append(r.frames, b)
	r.size += len(b)
	for len(r.frames) > 1 && r.size > most {
		r.size -= len(r.frames[0])
		r.frames[0] = nil
		r.frames = r.frames[1:]
	}
}

// take is what it holds, oldest first, and empties it.
func (r *ring) take() [][]byte {
	out := r.frames
	*r = ring{}
	return out
}

// unread puts back frames taken and not sent, before what it holds now.
func (r *ring) unread(frames [][]byte) {
	for _, b := range frames {
		r.size += len(b)
	}
	r.frames = append(slices.Clone(frames), r.frames...)
}

// quietSince is when the call was last not quiet (mu held): anyone spoke, the model's speech played out, it handed
// something over, the extension told it something, its session came up.
func (c *conversation) quietSince() time.Time {
	return latest(c.pace.playEnd, c.pace.voice, c.pace.delegated, c.gate.user, c.userAt, c.connectedAt, c.sus.said)
}

// pauseDue: the upstream may be paused now (mu held).
func (c *conversation) pauseDue(now time.Time) bool {
	switch {
	case !c.ready || c.up == nil || c.version < 2 || c.closed || !c.sus.works || c.sus.state != "":
		return false // ready first: the version is set before it, outside mu
	case len(c.queue) > 0 || len(c.pending) > 0 || c.askCur != "" || c.user != nil:
		return false // something to say, a handover waiting for its call, a card asked, the user's line open
	}
	return now.Sub(c.quietSince()) >= pauseAfter
}

// maybePause closes the upstream session when the call may pause (pauseDue).
func (c *conversation) maybePause(now time.Time) {
	c.mu.Lock()
	if !c.pauseDue(now) {
		c.mu.Unlock()
		return
	}
	s := &c.sus
	if time.Since(c.connectedAt) >= stableAfter {
		c.reconnects = 0 // as a drop of it would (lost): it held
	}
	s.state, s.at, s.voice, s.ring, s.texts, s.spoke = linkPaused, now, barge{}, ring{}, nil, false
	s.pauses++
	c.gen++ // its delegations go with it: what comes back for them is said
	c.ready = false
	up, w := c.up, c.w
	c.up = nil
	clear(c.unacked) // that session's: an ack or refusal of it never comes
	c.replied = nil
	c.dropAudio, c.cutAt, c.barge = false, time.Time{}, barge{}
	c.closeAsst()
	c.closeUser()
	c.sendApp(map[string]any{"type": "link", "state": linkPaused})
	c.logf("paused the upstream (%d): session %d closed, the conversation works and nobody spoke for %v", s.pauses, sessionNo(c.gen-1),
		now.Sub(c.quietSince()).Round(time.Second))
	c.mu.Unlock()
	go func() { // its reader sees the new gen and stops
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_ = writeJSON(ctx, up, w.close())
		cancel()
		_ = up.Close(websocket.StatusNormalClosure, "paused")
	}()
}

// setWorks notes whether the bound conversation works on something the call waits for (the tracker's look).
func (c *conversation) setWorks(works bool) {
	c.mu.Lock()
	c.sus.works = works
	c.mu.Unlock()
}

// paused: the upstream is paused or waking (the tracker says no heartbeat then).
func (c *conversation) paused() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sus.state != ""
}

// held keeps a frame of the app's audio, voiced or not, while the upstream is paused or waking (mu held): whether it
// was kept (it is not sent now). The user's voice for wakeVoice wakes a paused one.
func (c *conversation) held(b []byte, voiced bool) bool {
	s := &c.sus
	if s.state == "" {
		return false
	}
	rate := c.p.inputRate()
	s.ring.add(b, int(ringSpan.Seconds()*float64(rate))*2)
	if s.state == linkPaused && rate > 0 && s.voice.add(voiced, time.Duration(len(b)/2)*time.Second/time.Duration(rate)) >= wakeVoice {
		c.rouse("the user's voice")
	}
	return true
}

// holdText keeps a line the user typed while the upstream is paused or waking (mu held): whether it was kept. It is
// sent to the new session after the ring, and is what was said from then on, not before the pause (the session's
// instructions do not have it: remember happens as it is sent).
func (c *conversation) holdText(text string) bool {
	if c.sus.state == "" {
		return false
	}
	c.takeBack(text)
	c.sus.texts = append(c.sus.texts, text)
	c.rouse("a line the user typed")
	return true
}

// takeBack takes the user's line text back out of what was said, when it is the last of it (mu held).
func (c *conversation) takeBack(text string) {
	if n := len(c.history); n > 0 && c.history[n-1].Role == "user" && c.history[n-1].Text == text {
		c.history = c.history[:n-1]
	}
}

// rouse begins to wake a paused upstream (mu held); why is what woke it (the log).
func (c *conversation) rouse(why string) {
	s := &c.sus
	if s.state != linkPaused || c.closed {
		return
	}
	s.state, s.at = linkWaking, time.Now()
	c.sendApp(map[string]any{"type": "link", "state": linkWaking})
	c.logf("waking the upstream: %s", why)
	go c.wakeUp()
}

// wakeUp opens the new session (connect: its instructions tell it what was said and what is waited on, and link{live}
// goes once it is up), wakeMost tries in a row before the call ends; then sends it what the ring kept.
func (c *conversation) wakeUp() {
	for try := 0; ; try++ {
		if try > 0 {
			select {
			case <-c.ctx.Done():
				return
			case <-time.After(time.Duration(try) * wakeBackoff):
			}
		}
		err := c.connect(true)
		if err == nil {
			c.flushRing()
			return
		}
		if c.ctx.Err() != nil {
			return
		}
		c.mu.Lock()
		c.sus.failed++
		failed := c.sus.failed
		c.mu.Unlock()
		c.logf("wake (%d of %d) failed: %s", failed, wakeMost, failure(err))
		if failed >= wakeMost {
			c.failCode(codeUpstreamFailed, i18n.T(c.language(), "voice-suspend.wake_failed", i18n.Args{"reason": err.Error()}), true)
			return
		}
	}
}

// flushRing sends the woken session the app's audio kept meanwhile, then the lines typed, and makes the upstream live:
// audio and lines coming meanwhile are kept too (held, holdText) and go out with them, in order.
//
// A typed line counts as the user speaking as it reaches the session, as one typed on a live call does (readApp): it
// was typed before the session came up, and the session may have closed the user-speech gate then (connect: the agent
// wrote while paused, and its instructions have those words), but it is what the model acts on after them.
//
// If the session drops before all of it is out, the rest stays kept: lost reconnects, and the reconnected session gets
// it (lost calls this once it is up). One flush runs at a time; one that finds another running leaves it to that one,
// which goes on with whichever session is up.
func (c *conversation) flushRing() {
	c.mu.Lock()
	if c.sus.state != linkWaking || c.sus.flush {
		c.mu.Unlock()
		return // nothing kept (a reconnect after a drop of a live session), or another flush sends it
	}
	c.sus.flush = true
	c.mu.Unlock()
	for {
		c.mu.Lock()
		up, w, gen := c.up, c.w, c.gen
		if up == nil || c.closed {
			c.sus.flush = false
			c.mu.Unlock()
			return // the session dropped: lost reconnects, and the reconnected one gets what is kept
		}
		var out []any
		frames, text := c.sus.ring.take(), ""
		switch {
		case len(frames) > 0:
			if c.sus.spoke { // the user's voice in it reaches the session now, after what its instructions told it
				c.gate.voice = time.Now()
			}
			for _, b := range frames {
				out = append(out, w.audio(b))
			}
		case len(c.sus.texts) > 0:
			text, c.sus.texts = c.sus.texts[0], c.sus.texts[1:]
			c.remember("user", text, time.Now())
			c.gate.user = time.Now()
			c.gate.typed = c.gate.user
			out = append(out, w.text(text))
		default:
			c.sus.state, c.sus.flush, c.sus.failed, c.sus.spoke = "", false, 0, false
			c.logf("woke the upstream in %v", time.Since(c.sus.at).Round(10*time.Millisecond))
			c.mu.Unlock()
			select { // what waits to be said goes now
			case c.wake <- struct{}{}:
			default:
			}
			return
		}
		c.mu.Unlock()
		for i, f := range out {
			err := writeJSON(c.ctx, up, f)
			if err == nil {
				continue
			}
			c.mu.Lock()
			if text != "" {
				c.takeBack(text)
				c.sus.texts = append([]string{text}, c.sus.texts...)
			} else {
				c.sus.ring.unread(frames[i:])
			}
			c.mu.Unlock()
			c.lost(gen, err) // nothing when it was lost already
			c.mu.Lock()
			if c.up == up { // not taken off: the call is over, or out of reconnects (lost ends it)
				c.sus.flush = false
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
			break
		}
	}
}

// works: the bound conversation works on something the call waits for (protocol 2): it works (as the tracker last
// looked at it), or a run of the call's on it waits on its background work (held) or on something outside (blocked).
func (t *tracker) works() bool {
	if !t.obs.on {
		return false
	}
	return t.busy || slices.ContainsFunc(t.runs, func(r *voiceRun) bool {
		return linkKey{r.MachineID, r.SessionID} == t.obs.key && !r.told && (r.State == runHeld || r.State == runBlocked)
	})
}

// saw notes how a look found the conversation of key (s nil: not listed): whether the bound conversation works.
func (t *tracker) saw(key linkKey, s *proto.Session) {
	if t.obs.on && key == t.obs.key {
		t.busy = s != nil && atWork(s) // waiting on the user is not: they may speak at any moment (docs/assistant/voice.md 4.9)
	}
}
