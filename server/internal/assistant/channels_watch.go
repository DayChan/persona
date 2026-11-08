package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// watchChannelRun follows a run until it is answered: the conversation's items come to the assistant (followed), for
// the progress shown meanwhile.
func (s *Service) watchChannelRun(r *channel.Run) {
	c := &s.chans
	ctx := context.Background()
	cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	removed := s.machineRemoved(cctx, r.MachineID)
	cancel()
	c.mu.Lock()
	if c.live == nil {
		c.live = map[string]*chanLive{}
	}
	if c.live[r.ID] != nil {
		c.mu.Unlock()
		return // already watched
	}
	prog := &chanLive{mu: newChanMutex(), checking: newChanMutex(), done: make(chan struct{})}
	c.live[r.ID] = prog
	c.mu.Unlock()
	if removed {
		// The machine was removed: nothing will answer it. The run ends like any other, so the owner is told and the
		// message's reaction changes, rather than left on "OnIt" with no word. An answer that did not go out is tried
		// again (finishChannelRun gives up after channelAnswerTries and ends the run all the same).
		s.logf("channels: %s: its machine was removed", r.ID)
		res := &channel.Result{Note: i18n.T(s.botLang(ctx), "assistant-channels_runs.machine_gone"), End: channel.EndGone}
		for {
			s.finishChannelRun(r, res)
			select {
			case <-prog.done:
				return
			case <-time.After(channelGoneRetry):
			}
		}
	}
	fctx, fcancel := context.WithTimeout(ctx, 10*time.Second) // a node that never answers does not hold the watcher
	s.followSession(fctx, r.MachineID, r.SessionID)
	fcancel()
	defer s.unfollowSession(r.MachineID, r.SessionID)
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-prog.done:
			return
		case <-t.C:
		}
		if run, _ := s.channelRunByID(r.ID); run == nil { // dropped (unbound, folded into a later message)
			c.mu.Lock()
			if c.live[r.ID] == prog {
				delete(c.live, r.ID)
			}
			c.mu.Unlock()
			return
		}
		s.retryFollow(r.MachineID, r.SessionID)
		s.checkChannelRun(r.ID)
		s.channelProgressTick(r.ID)
	}
}

// sessionNow is a conversation as the hub lists it now; an error when the hub could not be asked.
func (s *Service) sessionNow(ctx context.Context, machine, session string) (proto.Session, bool, error) {
	var res struct {
		Session proto.Session `json:"session"`
		OK      bool          `json:"ok"`
	}
	if err := s.call(ctx, machine, DeviceSessionGet, map[string]any{"session_id": session}, &res); err != nil {
		return proto.Session{}, false, err
	}
	return res.Session, res.OK, nil
}

func working(c proto.Session) bool { return c.Status == proto.StatusWorking || c.Starting }

// channelProgressTick shows a run's progress when it is due: the progress message after a few seconds and then every
// few, "typing" again every few seconds.
func (s *Service) channelProgressTick(id string) {
	r, prog := s.channelRunByID(id)
	if r == nil || prog == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	sink, _ := s.sinkFor(ctx, r.Channel)
	c := &s.chans
	if ts, ok := sink.(channelTyping); ok {
		c.mu.Lock()
		now := c.clock()
		maybe := !prog.finished && now.Sub(prog.updated) >= channel.TypingEvery
		held := r.Held
		c.mu.Unlock()
		if !maybe {
			return
		}
		if held {
			if sess, _, err := s.sessionNow(ctx, r.MachineID, r.SessionID); err != nil || !working(sess) {
				return
			}
		}
		c.mu.Lock()
		prog.updated = now
		c.mu.Unlock()
		ts.typing(ctx, true)
		return
	}
	ps, ok := sink.(channelProgress)
	if !ok || !prog.mu.TryLock() {
		return
	}
	defer prog.mu.Unlock()
	c.mu.Lock()
	card, started, held := r.Ext["card"]+r.Ext["cot"], r.Started, r.Held
	c.mu.Unlock()
	after, idle := channel.ProgressAfter, channel.ProgressIdle
	if pa, ok := sink.(interface {
		progressAfter(*channel.Run) time.Duration
	}); ok {
		after = pa.progressAfter(r)
	}
	if pi, ok := sink.(interface{ progressIdle() time.Duration }); ok {
		idle = pi.progressIdle()
	}
	// A new message gets its card before the turn is seen working (it may wait behind another turn, or be starting);
	// a held run only once its background work starts a turn.
	if card == "" && held {
		sess, _, err := s.sessionNow(ctx, r.MachineID, r.SessionID)
		if err != nil || !working(sess) {
			return // nothing going on to show (a held run waiting on its background work)
		}
	}
	s.locateRun(ctx, r, prog)
	c.mu.Lock()
	now := c.clock()
	steps, live, dirty := append([]string{}, prog.steps...), prog.live, prog.dirty
	due := !prog.finished && (card == "" && now.Sub(started) >= after ||
		card != "" && (dirty && now.Sub(prog.updated) >= channel.ProgressEvery || now.Sub(prog.updated) >= idle))
	if due {
		prog.dirty, prog.updated = false, now
	}
	c.mu.Unlock()
	if !due {
		return
	}
	ps.progress(ctx, r, now.Sub(started), steps, live)
}

// SessionItems is session.items: new items of a conversation the runs follow; they note the steps.
func (s *Service) SessionItems(ctx context.Context, p SessionItems) {
	s.registerInlineViews(ctx, p)
	l := s.botLang(ctx) // before s.chans.mu
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.live) == 0 {
		return
	}
	for _, r := range c.st.Runs {
		if r.MachineID != p.MachineID || r.SessionID != p.SessionID {
			continue
		}
		if lp := c.live[r.ID]; lp != nil {
			noteRunItems(l, r, lp, p.Items, c.clock())
		}
	}
}

// noteRunItems notes a run's new items as its steps, only from its own message on: a message queued behind another
// turn would show that turn's steps. Under c.mu.
func noteRunItems(l i18n.Lang, r *channel.Run, lp *chanLive, items []proto.Item, now time.Time) {
	items = slices.DeleteFunc(slices.Clone(items), func(it proto.Item) bool { return it.Seq <= max(r.Base, lp.upTo) })
	if lp.from == 0 {
		i, ok := r.Find(items, nil)
		if !ok || len(items) == 0 {
			return
		}
		items, lp.from = items[i:], max(items[i].Seq, 1)
	}
	for _, it := range items {
		lp.upTo = it.Seq
		lp.liveNote(l, it, now)
		if st := botStep(l, it); st != "" {
			lp.steps = append(lp.steps, st)
			lp.dirty = true
		}
		if cotItem(it) && len(lp.items) < cotItemsMost {
			lp.items = append(lp.items, it)
			lp.dirty = true
		}
		if it.Kind == proto.KindText && it.Role == "assistant" {
			lp.live = ""
		}
	}
	if len(lp.steps) > 200 {
		lp.steps = lp.steps[len(lp.steps)-200:]
	}
}

// locateRun looks for a run's message in the conversation when the followed items have not shown it (it went in
// before the run followed the conversation, or the assistant restarted): one page after Base, every few seconds.
func (s *Service) locateRun(ctx context.Context, r *channel.Run, prog *chanLive) {
	l := s.botLang(ctx) // before s.chans.mu
	c := &s.chans
	c.mu.Lock()
	now := c.clock()
	due := prog.from == 0 && now.Sub(prog.looked) >= channel.ProgressEvery
	if due {
		prog.looked = now
	}
	base := r.Base
	c.mu.Unlock()
	if !due {
		return
	}
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if s.call(ctx, r.MachineID, DeviceSessionItems, map[string]any{"session_id": r.SessionID, "after_seq": base, "lazy_images": true}, &res) != nil {
		return
	}
	c.mu.Lock()
	noteRunItems(l, r, prog, res.Items, c.clock())
	c.mu.Unlock()
}

// SessionDelta is session.delta: the text a followed conversation is writing right now.
func (s *Service) SessionDelta(_ context.Context, p SessionDelta) {
	c := &s.chans
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.live) == 0 {
		return
	}
	for _, r := range c.st.Runs {
		if r.MachineID == p.MachineID && r.SessionID == p.SessionID {
			if lp := c.live[r.ID]; lp != nil && lp.live != p.Text {
				lp.live, lp.dirty = p.Text, true
			}
		}
	}
}
