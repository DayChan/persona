package voice

import (
	"fmt"
	"slices"
	"time"
)

// sentWords is what deliver sends, kept by its event id until the service acks or refuses it.
type sentWords struct {
	words             // text is what goes; short, for a service that refuses it as too long
	delegation string // the delegation it answers, of upstream session gen ("" said)
	gen        int
	agent      bool // an agent's words (gate.go)
	onCall     bool // a tool's result: on its call only, never said
	at         time.Time
	queued     time.Time // it went into the queue: the user's voice holds it back at most answerHoldMost / sayHoldMost
	prio       int       // which goes first of what waits for the insertion gate: higher first, then the oldest
	card       string    // the card it asks (asks.go): once it is sent, the model asks that card
	cards      []string  // the cards it says wait on the screen (asks.go): not said once they are all gone
	// run is the run it is about (a heartbeat: it goes when the run has more to say); expires, when it is not said by
	// then it is not said (a heartbeat gone stale).
	run     string
	expires time.Time
}

// deliver sends s once the insertion gate lets it (gate.go; a tool's result on its call goes at once).
func (c *conversation) deliver(s *sentWords) {
	if s.onCall {
		c.sendNow(s)
		return
	}
	c.mu.Lock()
	if c.closed || s.prio == prioHeartbeat && c.sus.state != "" { // no heartbeat while paused (suspend.go)
		c.mu.Unlock()
		return
	}
	s.queued = time.Now()
	c.queue = append(c.queue, s)
	c.rouse("something to say") // a paused upstream (suspend.go)
	c.mu.Unlock()
	select {
	case c.wake <- struct{}{}:
	default:
	}
}

// speaker sends what waits for the insertion gate as the gate lets it, and ends the user's open caption after
// captionQuiet without more of it (and without their voice).
func (c *conversation) speaker() {
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-c.wake:
		case <-tick.C:
		}
		for {
			s := c.nextToSay(time.Now())
			if s == nil {
				break
			}
			c.sendNow(s)
		}
		c.maybePause(time.Now()) // suspend.go
		c.mu.Lock()
		if now := time.Now(); c.user != nil && now.Sub(c.userAt) >= captionQuiet && now.Sub(c.pace.voice) >= captionQuiet {
			c.closeUser()
		}
		c.mu.Unlock()
	}
}

// nextToSay takes the first of the queue the insertion gate lets go now: the highest prio, then the oldest.
func (c *conversation) nextToSay(now time.Time) *sentWords {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.queue = slices.DeleteFunc(c.queue, func(s *sentWords) bool { return !s.expires.IsZero() && now.After(s.expires) })
	if !c.ready || c.sus.state != "" {
		return nil // no session to say it to (paused, waking, reconnecting): it waits for the next one
	}
	best := -1
	for i, s := range c.queue {
		say := s.delegation == "" || s.gen != c.gen
		if now.Before(c.pace.readyAt(say, s.queued)) {
			continue
		}
		if best < 0 || s.prio > c.queue[best].prio {
			best = i
		}
	}
	if best < 0 {
		return nil
	}
	s := c.queue[best]
	c.queue = slices.Delete(c.queue, best, best+1)
	return s
}

// sendNow sends s: as the answer on its delegation while that is open in its session, else as something to say. On a
// service that acks (liveWire) it goes with an event id and is kept until the ack, which closes the gate again when it
// holds an agent's words (gate.go), or until the service refuses it (serviceRefused). An agent's words close the gate
// as they are sent.
func (c *conversation) sendNow(s *sentWords) {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return
	}
	w, now := c.w, time.Now()
	if s.delegation != "" && s.gen != c.gen {
		s.delegation = "" // that session is gone, and its delegations with it
	}
	if s.delegation == "" && s.onCall {
		c.mu.Unlock()
		return
	}
	id := ""
	if w.acks() {
		c.events++
		id = fmt.Sprintf("persona-%d", c.events)
		s.at = now
		for k, o := range c.unacked {
			if now.Sub(o.at) > unackedMost {
				delete(c.unacked, k)
			}
		}
		c.unacked[id] = s
	}
	if s.agent {
		c.gate.told(now)
	}
	c.sus.said = now
	c.told(s.card)
	delegation, gen, text, onCall := s.delegation, s.gen, s.text, s.onCall
	c.mu.Unlock()
	if delegation != "" && c.sendUp(gen, w.reply(delegation, text, true, id)...) || onCall {
		return
	}
	if delegation != "" {
		c.mu.Lock()
		s.delegation = ""
		c.mu.Unlock()
	}
	c.sendUp(-1, w.say(text, id)...)
}

// serviceRefused handles the service refusing what deliver sent (mu held; docs/assistant/voice.md 4.5 rule 4). It names it by
// its event id; an error about a delegation id that names none is taken for the oldest still waiting on a delegation.
// Too long for this service (param content): sent again short. On a delegation that is gone (expired, closed, or any
// other refusal of it): said instead. A refused say is let go (logged).
func (c *conversation) serviceRefused(e event) {
	id := e.eventID
	if c.unacked[id] == nil && e.param == "delegation_id" {
		id = ""
		for k, o := range c.unacked {
			if o.delegation != "" && (id == "" || o.at.Before(c.unacked[id].at)) {
				id = k
			}
		}
	}
	s := c.unacked[id]
	if s == nil {
		return
	}
	delete(c.unacked, id)
	again := *s
	switch {
	case e.param == "content" && s.text != s.short:
		again.text = s.short
		c.logf("upstream: the service refused %s as too long: sending it again shorter", id)
	case s.delegation != "" && !s.onCall:
		again.delegation = ""
		c.logf("upstream: the service refused %s on its delegation %s: saying it instead", id, s.delegation)
	default:
		c.logf("upstream: the service refused %s for good: dropped", id)
		return
	}
	go c.deliver(&again)
}
