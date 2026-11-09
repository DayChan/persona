package assistant

import (
	"context"
	"net/url"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- the tick ----

// workLoop runs the tick until ctx ends.
func (s *Service) workLoop(ctx context.Context) {
	t := time.NewTicker(workEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			s.checkToolLoops(ctx)
			if s.workOn(ctx) {
				s.workTick(ctx, now)
			}
		}
	}
}

// workTick: due wakes go out, cards past their day go, turns that ended unseen while the manager was away (or whose
// triage failed a while ago) are triaged, and the number of unread sessions is sampled.
func (s *Service) workTick(ctx context.Context, now time.Time) {
	if !s.loadWork(ctx) {
		return
	}
	v := s.newWorkView(ctx)
	s.work.mu.Lock()
	s.work.init()
	var due, waits, cards [][2]string
	agents := map[[2]string]proto.WorkAgent{}
	for k, e := range s.work.entries {
		switch {
		case e.WakeAt != nil && !e.WakeAt.After(now):
			due = append(due, k)
		case e.WakeAt != nil && e.Work.On == proto.WaitAgent && e.Work.Agent != nil:
			waits, agents[k] = append(waits, k), *e.Work.Agent
		case e.InteractionID != "":
			cards = append(cards, k)
		}
	}
	sample := now.Sub(s.work.sampleAt) >= workSampleGap
	if sample {
		s.work.sampleAt = now
	}
	prune := now.Sub(s.work.pruneAt) >= 24*time.Hour
	if prune {
		s.work.pruneAt = now
	}
	backfill := now.Sub(s.work.backfillAt) >= workBackfillEvery
	if backfill {
		s.work.backfillAt = now
	}
	s.work.mu.Unlock()
	for _, k := range waits {
		if s.agentStopped(v, agents[k]) {
			due = append(due, k)
		}
	}
	for _, k := range due {
		if e, ok := s.workEntry(k); ok && e.Work.CI != nil {
			s.startCIPoll(ctx, k, now)
			continue
		}
		s.workWake(ctx, v, k, now)
	}
	for _, k := range cards {
		s.cardTick(ctx, v, k, now)
	}
	if backfill {
		s.backfillWork(ctx, v, now)
	}
	if sample {
		s.sampleWork(v)
	}
	if st := s.workDB(); prune && st != nil {
		if err := st.PruneSessionWork(ctx, now.Add(-workKeep)); err != nil {
			s.logf("work: prune: %v", err)
		}
	}
}

// agentStopped: the agent a session waits for is not working any more (or is gone).
func (s *Service) agentStopped(v *workView, a proto.WorkAgent) bool {
	if _, ok := v.device(a.MachineID); !ok {
		return false // its machine is away: the fallback wake decides
	}
	c, ok := v.session(a.MachineID, a.SessionID)
	return !ok || c.Status != proto.StatusWorking
}

// wakeWaitersOn: sid on machine stopped; agents waiting for it are due now.
func (s *Service) wakeWaitersOn(machine, sid string) {
	s.work.mu.Lock()
	defer s.work.mu.Unlock()
	s.work.init()
	now := time.Now().UTC()
	for _, e := range s.work.entries {
		if a := e.Work.Agent; e.WakeAt != nil && e.Work.On == proto.WaitAgent && a != nil && a.MachineID == machine && a.SessionID == sid {
			e.WakeAt, e.Work.WakeAt = &now, &now
		}
	}
}

// workGuard: the manager may act on e now — the session is still stopped on the same turn, nothing pending or queued,
// not looked at in herdr, its machine up, and its todo not completed. ownCard: the manager's card for it may be pending.
func (s *Service) workGuard(v *workView, e db.SessionWork, ownCard bool) (proto.Session, string) {
	if d, ok := v.device(e.MachineID); !ok || !d.Reachable {
		return proto.Session{}, "offline"
	}
	c, ok := v.session(e.MachineID, e.SessionID)
	if v.failed[e.MachineID] {
		return c, "offline" // the hub could not say: nothing is decided on it now
	}
	closed, err := s.completedTodo(v.ctx, e.MachineID, e.SessionID)
	if err != nil {
		return c, "todo state unavailable"
	}
	if closed {
		return c, "task done"
	}
	switch {
	case !ok:
		return c, "gone"
	case c.ReadOnlyReason != "":
		return c, "read-only"
	case c.Status == proto.StatusWorking || !sameTurn(c, e.TurnAt):
		return c, "moved on"
	case c.Status == proto.StatusIdle:
		return c, "looked at in herdr"
	case c.Status != proto.StatusDone:
		return c, "not stopped"
	case !ownCard && sessionTurnErrorCode(c) == proto.TurnErrorModelFallbackUnavailable:
		return c, "refusal fallback needs recovery" // also cancels a continuation queued before the fallback was known
	case len(c.Queued) > 0:
		return c, "input queued"
	case c.PendingInteractions > 0 && !(ownCard && c.PendingInteractions == 1 && e.InteractionID != ""):
		return c, "asking"
	}
	return c, ""
}

// workWake sends what is due for k: "Continue" after a transient error, the continue once a usage limit reset, or a
// wake for a wait.
func (s *Service) workWake(ctx context.Context, v *workView, k [2]string, now time.Time) {
	e, ok := s.workEntry(k)
	if !ok || e.WakeAt == nil {
		return
	}
	// Read again right before acting: the pass's view may be a send or two old.
	c, why := s.workGuard(s.newWorkView(ctx), e, false)
	switch why {
	case "":
	case "offline", "todo state unavailable":
		before := e
		at := now.Add(time.Minute).UTC() // try again when it is back
		e.WakeAt, e.Work.WakeAt = &at, &at
		if before.Work.CI != nil {
			s.replaceCI(before, e)
		} else {
			s.putWork(ctx, e)
		}
		return
	default:
		s.logf("work %s/%s: not woken: %s", k[0], short(k[1]), why)
		before := e
		e.WakeAt, e.Work.WakeAt = nil, nil
		if before.Work.CI != nil {
			e.Work.CI = nil
			s.replaceCI(before, e)
		} else {
			s.putWork(ctx, e)
		}
		return
	}
	sw := s.triageSwitches(ctx)
	if continues(e.Work) && !sw.AutoContinue || !continues(e.Work) && !sw.Wake {
		before := e
		e.WakeAt, e.Work.WakeAt = nil, nil // turned off since it was set
		if before.Work.CI != nil {
			e.Work.CI = nil
			s.replaceCI(before, e)
		} else {
			s.putWork(ctx, e)
		}
		return
	}
	if e.Work.CI != nil {
		closed, _, err := s.ciClosedTodo(ctx, e.MachineID, e.SessionID, e.Work.CI.TodoProof, proto.WorkState{}, time.Time{})
		if err != nil || closed {
			current := e
			if err != nil {
				at := now.Add(time.Minute).UTC()
				current.WakeAt, current.Work.WakeAt = &at, &at
			} else {
				current.WakeAt, current.Work.WakeAt, current.Work.CI = nil, nil, nil
			}
			s.replaceCI(e, current)
			return
		}
	}
	if e.Work.CI != nil && e.Work.CI.Message != "" {
		_, guest, err := s.guestOf(ctx, e.MachineID, e.SessionID)
		if err != nil || guest || s.isWorkMachine(e.MachineID) {
			current := e
			at := e.Work.CI.FallbackAt
			if at.Before(now) {
				at = now.UTC()
			}
			current.Work.CI = nil
			current.WakeAt, current.Work.WakeAt = &at, &at
			s.replaceCI(e, current)
			return
		}
		s.deliverCI(ctx, e, now)
		return
	}
	polled := e
	ciLine, handled := s.pollCI(ctx, polled, c, now)
	if handled {
		return
	}
	if ciLine != "" {
		var still bool
		e, still = s.workEntry(k)
		if !still || !sameCIWatch(polled, e) {
			return
		}
		if latest, why := s.workGuard(s.newWorkView(ctx), e, false); why != "" {
			return
		} else {
			c = latest
		}
	}
	var text, kind string
	switch {
	case e.Work.ResetsAt != nil:
		if !s.limitStillDue(ctx, &e, c, now) {
			s.putWork(ctx, e)
			return
		}
		e.Retries++
		text, kind = prompts.RenderAgentLimitContinue(), "limit_continued"
	case e.Work.On == proto.WaitTransient:
		e.Retries++
		text, kind = continueText, "continued"
	case e.Work.On == proto.WaitExternal || e.Work.On == proto.WaitAgent:
		e.Wakes++
		wk := agentWake{Attempt: e.Wakes, Wait: e.Work.Wait, Then: e.Work.Then}
		s.work.mu.Lock()
		if a, ok := s.work.approved[k]; ok && risky(e.Work.Then) && a.covers(&e.Work) {
			wk.Approved = a.then // the user said yes to it: the wake must not tell it to ask again
		}
		s.work.mu.Unlock()
		if a := e.Work.Agent; a != nil && s.agentStopped(v, *a) {
			wk.Agent = a.Label
			if as, ok := v.session(a.MachineID, a.SessionID); ok {
				wk.AgentLine = clip(oneLine(as.LastText), 200)
			}
		}
		text, kind = renderWake(wk), "woke"
		if ciLine != "" {
			text = ciLine + "\n\n" + text
		}
	default:
		return
	}
	if ciLine != "" {
		if s.prepareCI(ctx, e, text) {
			if pending, ok := s.workEntry(k); ok {
				s.deliverCI(ctx, pending, now)
			}
		}
		return
	}
	e.WakeAt, e.Work.WakeAt = nil, nil
	s.putWork(ctx, e)
	if err := s.sendToSession(ctx, k[0], c.SessionID, text); err != nil {
		s.logf("work %s/%s: %s: %v", k[0], short(k[1]), kind, err)
		at := now.Add(2 * time.Minute).UTC()
		e.WakeAt, e.Work.WakeAt = &at, &at
		if kind == "woke" {
			e.Wakes--
		} else {
			e.Retries--
		}
		s.putWork(ctx, e)
		return
	}
	s.logf("work %s/%s: %s (%d)", k[0], short(k[1]), kind, max(e.Retries, e.Wakes))
	s.workEvent(k[0], k[1], kind, map[string]any{"attempt": max(e.Retries, e.Wakes), "after": now.Sub(e.TurnAt).Seconds()})
}

// continues: w's wake goes on with what the turn was doing ("Continue" after a transient error, or once a usage limit
// reset): the auto_continue switch's, not the wake switch's.
func continues(w proto.WorkState) bool { return w.On == proto.WaitTransient || w.ResetsAt != nil }

// limitStillDue: e's continue after a usage limit goes now, c being its session as read right before. Not when the
// CLI now says it goes on by itself (typing would cancel that): the wake is dropped. Not when the limit resets later
// than the wait was set for (the session says so now): it is set again for then. Either way e is changed.
func (s *Service) limitStillDue(ctx context.Context, e *db.SessionWork, c proto.Session, now time.Time) bool {
	if c.TurnErrorResumes {
		s.logf("work %s/%s: not continued: it goes on by itself after the usage limit", e.MachineID, short(e.SessionID))
		e.WakeAt, e.Work.WakeAt = nil, nil
		return false
	}
	if reset, ok := usageLimitReset(c); ok && reset.After(now) {
		at := limitContinueAt(reset, now)
		e.WakeAt, e.Work.WakeAt, e.Work.ResetsAt = &at, &at, &reset
		e.Work.Line = limitLine(s.lang(ctx), reset, false, now, s.location(ctx))
		s.logf("work %s/%s: the usage limit resets at %s now: continued then", e.MachineID, short(e.SessionID), reset.UTC().Format(time.RFC3339))
		return false
	}
	return true
}

// cardsUp are the manager's cards the hub has up now, by id (none when it cannot say).
func (s *Service) cardsUp(ctx context.Context) map[string]proto.Interaction {
	var res struct {
		Interactions []proto.Interaction `json:"interactions"`
	}
	out := map[string]proto.Interaction{}
	if err := s.call(ctx, DeviceAll, DeviceInteractionsList, map[string]any{}, &res); err != nil {
		s.logf("work: the hub's cards: %v", err)
		return out
	}
	for _, ix := range res.Interactions {
		if ix.ToolName == workCardTool {
			out[ix.ID] = ix
		}
	}
	return out
}

// cardBack is e's card put back (its turn still waits; nil when not now), and whether e keeps it meanwhile (false: past
// its day, or the turn went on). Only the agent going on ends the card: working again, or a later turn. A card
// still waits for its answer when the session was looked at (as one the hub kept up does), and is kept for a later
// tick while its machine or session is not back as it was — right after a hub restart the nodes reconnect and report
// their sessions over the next seconds, a node that restarted too before it has read the transcripts.
func (s *Service) cardBack(v *workView, e db.SessionWork, now time.Time) (*proto.Interaction, bool) {
	if now.After(e.TurnAt.Add(workCardTTL)) || len(e.Work.Questions) == 0 {
		return nil, false
	}
	c, why := s.workGuard(v, e, true)
	switch why {
	case "", "looked at in herdr":
		ix := workCard(e.MachineID, c, e.Work, e.InteractionID)
		return &ix, true
	case "task done":
		return nil, false
	case "moved on":
		if wentOn(c, e) {
			return nil, false
		}
	}
	return nil, true
}

// wentOn: c went on from e's turn — it works again, or a later turn ended. A session that reads older than the turn
// (a node that has not read its transcript yet) did not.
func wentOn(c proto.Session, e db.SessionWork) bool {
	return c.Status == proto.StatusWorking || c.UpdatedAt.After(e.TurnAt.Add(2*time.Second))
}

// cardTick: k's card is past its day, or its turn went on (a turn_started the manager missed while it was away): it
// goes, and the hub withdraws it when the snapshot no longer has it. One read back and not out yet is put back out.
func (s *Service) cardTick(ctx context.Context, v *workView, k [2]string, now time.Time) {
	e, ok := s.workEntry(k)
	if !ok || e.InteractionID == "" {
		return
	}
	s.work.mu.Lock()
	_, out := s.work.cards[e.InteractionID]
	s.work.mu.Unlock()
	var drop bool
	if out {
		c, why := s.workGuard(v, e, true)
		drop = now.After(e.TurnAt.Add(workCardTTL)) || why == "task done" || why == "moved on" && wentOn(c, e)
	} else {
		ix, keep := s.cardBack(v, e, now)
		if ix != nil {
			s.work.mu.Lock()
			current, exists := s.work.entries[k]
			if exists && current.InteractionID == ix.ID {
				s.work.cards[ix.ID] = *ix
			}
			s.work.mu.Unlock()
			s.publishSoon()
			return
		}
		drop = !keep
	}
	if !drop {
		return
	}
	s.work.mu.Lock()
	delete(s.work.cards, e.InteractionID)
	s.work.mu.Unlock()
	if !out {
		s.recordEnd(e.InteractionID, "cancelled", nil) // never put back up: nothing else ends it in the history
	}
	e.InteractionID, e.Work.InteractionID = "", ""
	s.putWork(ctx, e)
}

// backfillWork triages turns that ended unseen while the manager could not hear of them (unread, within a day), a
// few per pass.
func (s *Service) backfillWork(ctx context.Context, v *workView, now time.Time) {
	n := 0
	settle := settleOf(s.triageCfg(ctx))
	for _, d := range v.machines() {
		if !d.Reachable {
			continue
		}
		for _, c := range v.sessions(d.ID) {
			if n >= 3 {
				return
			}
			if c.Assistant || c.State != proto.StateUnread || c.Work != nil || now.Sub(c.UpdatedAt) > workBackfill ||
				now.Sub(c.UpdatedAt) < settle {
				continue
			}
			k := [2]string{d.ID, c.SessionID}
			s.work.mu.Lock()
			tried, ok := s.work.tried[k]
			s.work.mu.Unlock()
			if ok && sameTurn(c, tried.turn) && now.Sub(tried.at) < workRetryTriage {
				continue // tried on this turn a moment ago (a failed model call is not retried every pass)
			}
			n++
			go s.triageTurn(context.WithoutCancel(ctx), s.newWorkView(ctx), d.ID, c) // a model call each: the wakes do not wait
		}
	}
}

// sampleWork records how many sessions are in each state now (for the stats: "Unread" over time).
func (s *Service) sampleWork(v *workView) {
	counts := map[string]any{}
	for _, d := range v.machines() {
		for _, c := range v.sessions(d.ID) {
			if c.Assistant || c.State == proto.StateOffline {
				continue
			}
			n, _ := counts[c.State].(int)
			counts[c.State] = n + 1
		}
	}
	s.workEvent("", "", "sample", counts)
}

// httpsLink: u is an http(s) link worth putting on a card.
func httpsLink(u string) bool {
	p, err := url.Parse(strings.TrimSpace(u))
	return err == nil && (p.Scheme == "https" || p.Scheme == "http") && p.Host != ""
}
