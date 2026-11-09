package assistant

import (
	"context"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// The manager's reminder of an agent still waiting on the user's answer (docs/protocol/agent-states.md "Awaiting your reply", #5096): the
// user looked at an agent that stopped on a question (Session.AwaitsReply) and moved on. Once it has waited
// replyAfter since they looked, one push says it still waits — what it asks, answerable from the notification. Rules
// only: no model is asked and no conversation started, so it costs no tokens and is on unless the user turns it off
// (Settings › Session Triage). A turn is reminded of once; answering it (the agent works again) ends it.

// replyAfter is how long an agent waits after the user looked at its question before the reminder, unless set.
const replyAfter = 30 * time.Minute

// replyEvery is how often the waiting agents are looked at.
const replyEvery = time.Minute

// replyOldest: a question older than this is not reminded of (the user let it go).
const replyOldest = 24 * time.Hour

// replyBook is which questions were reminded of, in memory. A question is its session's turn end (Session.UpdatedAt)
// and what it asks: the same words with a moved UpdatedAt (a line written to the transcript after the turn, its file
// touched) are the same question. The manager restarting forgets it: the first list of each machine it looks at takes
// the questions already due as reminded (no burst of pushes after a deploy, whenever each node comes back).
type replyBook struct {
	mu       sync.Mutex
	reminded map[[2]string]replyMark
	primed   map[string]bool // the machines whose list was looked at
}

type replyMark struct {
	turn time.Time
	ask  string
}

// same: sess still asks the question m was about. Its words are enough: a mark is dropped as soon as its agent works
// again (replyWorked), so the same words after an answer are a new question.
func (m replyMark) same(sess proto.Session) bool {
	ask := replyAsk(sess)
	return m.turn.Equal(sess.UpdatedAt) || ask != "" && m.ask == ask
}

// replyOn: the reminders are on (the switch unset counts as on), and after how long.
func (sw TriageSwitches) replyOn() (bool, time.Duration) {
	after := replyAfter
	if sw.ReplyReminderMinutes > 0 {
		after = time.Duration(sw.ReplyReminderMinutes) * time.Minute
	}
	return sw.ReplyReminder == nil || *sw.ReplyReminder, after
}

func (s *Service) replyLoop(ctx context.Context) {
	t := time.NewTicker(replyEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			if !s.fleetOn() {
				continue
			}
			on, after := s.triageSwitches(ctx).replyOn()
			if !on {
				s.replyOff()
				continue
			}
			s.replyTick(now, after)
		}
	}
}

// replyWorked: the agent works again (the user answered, or it went on): its question is no longer the one reminded of.
func (s *Service) replyWorked(machine, sid string) {
	b := &s.reply
	b.mu.Lock()
	delete(b.reminded, [2]string{machine, sid})
	b.mu.Unlock()
}

// shownList is machine's list as last told the hub (decorated), or decorated now when none was told yet.
func (s *Service) shownList(machine string) []proto.Session {
	f := &s.fleet
	f.shownMu.Lock()
	list, ok := f.shown[machine]
	f.shownMu.Unlock()
	if !ok {
		return s.decorated(machine)
	}
	return list
}

// replyOff: the reminders are off. Turned on again, each machine's first look takes what is due by then as reminded
// (no burst of what waited meanwhile).
func (s *Service) replyOff() {
	b := &s.reply
	b.mu.Lock()
	b.primed = nil
	b.mu.Unlock()
}

// replyDue is an agent whose question is due a reminder.
type replyDue struct {
	machine string
	sess    proto.Session
}

// replyTick reminds of the questions that waited after since the user looked.
func (s *Service) replyTick(now time.Time, after time.Duration) {
	s.loadSeen(context.Background()) // read once, tried again while it fails
	s.fleet.mu.Lock()
	seenLoaded := s.fleet.seenLoaded
	s.fleet.mu.Unlock()
	if !seenLoaded {
		return // when each was looked at is not read back yet: every wait would look as old as its question
	}
	triage := s.workOn(context.Background())
	type look struct {
		machine string
		first   bool
		due     []proto.Session
	}
	var looks []look
	b := &s.reply
	for _, m := range s.fleetMachines() {
		asks := false // whether one may ask, read in place: the list is not copied
		if _, listed := s.nodeSessionsWhere(m.ID, func(c *proto.Session) bool {
			asks = asks || mayAsk(c, triage)
			return false
		}); listed == 0 {
			continue // nothing listed yet (the node not back): its first look is still to come
		}
		b.mu.Lock()
		first := !b.primed[m.ID]
		b.mu.Unlock()
		l := look{machine: m.ID, first: first}
		if asks { // decorate only where one may ask
			for _, sess := range s.shownList(m.ID) {
				if s.replyWaits(m.ID, sess, now) && now.Sub(s.replySince(m.ID, sess)) >= after {
					l.due = append(l.due, sess)
				}
			}
		}
		looks = append(looks, l)
	}
	b.mu.Lock()
	if b.reminded == nil {
		b.reminded = map[[2]string]replyMark{}
	}
	if b.primed == nil {
		b.primed = map[string]bool{}
	}
	for k, m := range b.reminded {
		if now.Sub(m.turn) > replyOldest {
			delete(b.reminded, k) // past reminding anyway
		}
	}
	var send []replyDue
	for _, l := range looks {
		b.primed[l.machine] = true
		for _, sess := range l.due {
			k := [2]string{l.machine, sess.SessionID}
			if m, ok := b.reminded[k]; ok && m.same(sess) {
				continue
			}
			b.reminded[k] = replyMark{turn: sess.UpdatedAt, ask: replyAsk(sess)}
			if !l.first {
				send = append(send, replyDue{l.machine, sess})
			}
		}
	}
	b.mu.Unlock()
	for _, d := range send {
		if err := s.remindReply(d.machine, d.sess); err != nil {
			s.logf("reply reminder %s/%s: %v", d.machine, short(d.sess.SessionID), err)
			b.mu.Lock()
			delete(b.reminded, [2]string{d.machine, d.sess.SessionID}) // tried again next minute (a hub that is behind)
			b.mu.Unlock()
		}
	}
}

// mayAsk: a node's session that may be awaiting a reply once decorated (stopped, and asking or not read by the node).
// With the triage on, any stopped one may be waiting on the user by its verdict.
func mayAsk(sess *proto.Session, triage bool) bool {
	if sess.Status != proto.StatusDone && sess.Status != proto.StatusIdle {
		return false
	}
	return triage || sess.Asks != "" || !sess.AsksRead && sess.LastText != ""
}

// replyWaits: sess is an agent the user looked at that still waits on their answer, and one to remind of: not muted,
// not one whose news goes elsewhere (an inbox draft, a Feishu thread, a quiet task's conversation), not too old.
func (s *Service) replyWaits(machine string, sess proto.Session, now time.Time) bool {
	if !sess.AwaitsReply || sess.State != proto.StateIdle || now.Sub(sess.UpdatedAt) > replyOldest ||
		spaceOf(sess) == SpaceHealth { // its questions are not for the lock screen (doneAlert)
		return false
	}
	return !s.mutedAs(machine, sess.SessionID, sess.SpaceName) && !s.isInboxAgent(sess.SessionID) &&
		!s.channelAnsweredPush(sess.SessionID) && !s.quietSession(machine, sess.SessionID)
}

// replySince is when sess's question started waiting on the user: when they looked at it in an app, or (seen on the
// computer, herdr idle) when it was asked.
func (s *Service) replySince(machine string, sess proto.Session) time.Time {
	if seen := s.seenAt(machine, sess.SessionID); seen.After(sess.UpdatedAt) {
		return seen
	}
	return sess.UpdatedAt
}

// remindReply pushes that sess still waits on the user's answer, to the devices that take Needs You pushes.
func (s *Service) remindReply(machine string, sess proto.Session) error {
	head, body := replyAlert(s.lang(context.Background()), sess)
	s.logf("reply reminder %s/%s: %s", machine, short(sess.SessionID), body)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return s.hub.NotifyRequest(ctx, NotifyRequest{Notification: Notification{ID: "ntf_" + randomHex(8),
		Kind: assistantwire.NotifyReply, DeviceID: machine, SessionID: sess.SessionID, Subtitle: s.pushPlace(machine, sess),
		Title: head, Body: body}})
}

// replyAlert is the reminder's title and text, in l: the agent's name, and what it asks.
func replyAlert(l i18n.Lang, sess proto.Session) (head, body string) {
	title := pushTitle(sess)
	if body = replyAsk(sess); body == "" {
		body = workT(l, "reply_body")
	}
	return workT(l, "reply_title", i18n.Args{"agent": title}), body
}

// replyAsk is what an awaiting agent asks, as its row and its reminder say it: what the triage says it waits on the
// user for, else the question its reply put.
func replyAsk(sess proto.Session) string {
	if w := sess.Work; w != nil && w.Phase == proto.WorkBlocked && w.Line != "" {
		return w.Line
	}
	return sess.Asks
}
