package assistant

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/kb"
)

// ---- choosing what runs ----

// historyBlock is why a configured source does not run now ("" it can), as the selection sel reads now: "widened"
// when it reads more than the owner approved (the caller takes the approval back). info is what the hub says the
// connector offers (nil: not asked yet, so whether its anchors are still to gather is not known).
func (d *historyDriver) historyBlock(src assistantwire.KBHistorySource, info *assistantwire.ArchiveSource, sel *kb.Selection, ret kb.Retention) (string, *historyPlan) {
	if src.SearchIndexNeeded() && !d.indexed() {
		return "needs_search_index", nil
	}
	p, ok, err := loadHistoryPlan(d.data, src.Name)
	switch {
	case err != nil:
		return "plan_unreadable", nil
	case !ok:
		return "no_plan", nil
	case p.ApprovedAt == nil:
		return "not_approved", p
	case sel.Stopgap(): // what the selection reads is not known now: it waits, the approval kept
		return "selection_unavailable", p
	}
	if n, unlisted := p.widened(sel, ret); n > 0 || unlisted {
		return "widened", p
	}
	if d.finished(src.Name, p, info, sel, ret) {
		return "done", p
	}
	return "", p
}

// finished: every chat the selection reads is done, in error or unreadable, for what it reads now; and for a source
// that can search, its anchors are gathered (they may add chats) unless no chat is read by them.
func (d *historyDriver) finished(source string, p *historyPlan, info *assistantwire.ArchiveSource, sel *kb.Selection, ret kb.Retention) bool {
	personal := false
	for _, r := range p.reading(sel, ret) {
		if !historyChatDone(d.data, kb.ChatID(r.chat.filing(source), r.chat.ID), r.need) {
			return false
		}
		personal = personal || !r.need.Whole
	}
	if info != nil && info.HistoryAnchors && (personal || historyWantsUnlisted(sel, ret, source)) {
		if cache, err := loadHistoryAnchors(d.data, source); err != nil || !cache.Done {
			return false
		}
	}
	return true
}

// historyChatDone: the chat's import is over for need (its state file read as it is, without opening the knowledge
// base): finished under a plan that covers it.
func historyChatDone(data, chatID string, need kb.ReadPlan) bool {
	var hs kb.HistoryState
	ok, err := readJSONFile(historyStatePathFor(data, chatID), &hs)
	return err == nil && ok && hs.Finished() && historyCovers(hs.Plan, need)
}

// historyCovers: a read under plan covers need (nil plan: a state from before plans were kept, covering nothing).
func historyCovers(plan *kb.ReadPlan, need kb.ReadPlan) bool {
	return plan != nil && !need.Wider(*plan)
}

// revokeWidened takes the approval of the source's plan back when the selection now reads more than was approved,
// saying how many chats that is (0: nothing taken back).
func (d *historyDriver) revokeWidened(source string, sel *kb.Selection, ret kb.Retention) (int, error) {
	p, ok, err := loadHistoryPlan(d.data, source)
	if err != nil || !ok {
		return 0, err
	}
	n, unlisted := p.widened(sel, ret)
	if n == 0 && !unlisted {
		return 0, nil
	}
	p.revoke()
	if err := saveHistoryPlan(d.data, p); err != nil {
		return 0, err
	}
	return max(n, 1), nil
}

// runOnce runs the sources that can run now, one after another under one lock and one backup, until none can.
func (d *historyDriver) runOnce(ctx context.Context) error {
	cfg := d.cfg(ctx)
	if !cfg.On() {
		return nil
	}
	if _, stopped := readHistoryStop(d.data); stopped {
		return nil
	}
	e, err := d.eng(ctx)
	if err != nil {
		return err
	}
	d.zone = e.Store.Zone()
	sel, ret := historySelection(e)
	// Approved plans first (no call at all otherwise), then what the connectors offer, then what is left to do. A plan
	// the selection now reads more of than was approved loses its approval, and the owner is told.
	var approved []assistantwire.KBHistorySource
	for _, src := range cfg.Sources {
		why, _ := d.historyBlock(src, nil, sel, ret)
		if why == "widened" {
			d.source = src.Name
			if n, err := d.revokeWidened(src.Name, sel, ret); err == nil && n > 0 {
				h := halt(false, "plan_changed", "count", strconv.Itoa(n))
				d.log(historyLogLine{Event: "end", historyReason: h.why})
				if d.notify != nil {
					d.notify(ctx, src.Name, h.why)
				}
			}
			d.source = ""
			continue
		}
		if why == "" || why == "done" {
			approved = append(approved, src)
		}
	}
	if len(approved) == 0 {
		return nil
	}
	list, err := d.src.Archives(ctx)
	if err != nil {
		return fmt.Errorf("the connectors: %w", err)
	}
	infos := map[string]assistantwire.ArchiveSource{}
	for _, a := range list {
		if a.History {
			infos[a.ID] = a
		}
	}
	var runnable []assistantwire.KBHistorySource
	for _, src := range approved {
		info, ok := infos[src.Name]
		if !ok {
			d.setStatus(src.Name, func(st *historySourceStatus) { st.State = "unavailable" })
			continue
		}
		if why, _ := d.historyBlock(src, &info, sel, ret); why == "" {
			runnable = append(runnable, src)
		}
	}
	if len(runnable) == 0 {
		return nil
	}
	d.zone = e.Store.Zone()
	if d.key, err = historyLogKey(d.data); err != nil {
		return err
	}
	now := d.now()
	lock, took, err := lockHistory(d.data, d.instance, now)
	if err != nil {
		if errors.Is(err, errHistoryLocked) || errors.Is(err, errHistoryLeased) {
			return nil // another process imports
		}
		return err
	}
	d.lock = lock
	defer func() {
		d.lock.release()
		d.lock = nil
		d.running.Store(nil)
	}()
	if took != nil {
		d.log(historyLogLine{Event: "takeover", historyReason: reason("stale_lease", "pid", strconv.Itoa(took.PID), "heartbeat", took.Heartbeat.UTC().Format(time.RFC3339))})
	}
	// The import rewrites day files: the data directory is backed up first, and nothing runs without a backup.
	path, err := backupKB(d.data, now)
	if err != nil {
		d.log(historyLogLine{Event: "backup", Result: "failed", historyReason: reason("backup_failed", "class", errClass(err))})
		return fmt.Errorf("backup before the import: %w", err)
	}
	d.log(historyLogLine{Event: "backup", Result: "ok", historyReason: reason("backup", "file", filepath.Base(path))})
	for _, src := range runnable {
		info := infos[src.Name]
		sel, ret := historySelection(e)
		if why, _ := d.historyBlock(src, &info, sel, ret); why != "" { // the plan or the selection changed while another source ran
			continue
		}
		err := d.runSource(ctx, src, info)
		var h *historyHalt
		switch {
		case err == nil:
		case errors.As(err, &h):
			switch {
			case h.stop:
				d.stopAll(ctx, h.why)
			default:
				d.log(historyLogLine{Event: "end", historyReason: h.why})
				if h.why.Code == "plan_changed" && d.notify != nil {
					d.notify(ctx, d.source, h.why)
				}
				stop, stopped := readHistoryStop(d.data)
				d.setStatus(src.Name, func(st *historySourceStatus) {
					st.State, st.Until = "idle", time.Time{}
					if stopped { // a STOP made by hand or by kb_history_stop
						at := stop.At
						if at.IsZero() {
							at = d.now().UTC()
						}
						st.State, st.LastStop = "stopped", &historyEvent{At: at, historyReason: stop.historyReason}
					}
				})
			}
			return nil
		default:
			return err
		}
	}
	return nil
}

// stopAll stops the whole import for a person to look: STOP, a log line, the status and a push.
func (d *historyDriver) stopAll(ctx context.Context, why historyReason) {
	now := d.now()
	if err := writeHistoryStop(d.data, historyStop{At: now.UTC(), historyReason: why, By: "driver", Source: d.source}); err != nil {
		d.logf("kb history: write STOP: %v", err)
	}
	d.log(historyLogLine{Event: "stop", historyReason: why})
	d.setStatus(d.source, func(st *historySourceStatus) {
		st.State = "stopped"
		st.LastStop = &historyEvent{At: now.UTC(), historyReason: why}
		if d.pacer != nil {
			st.Pace = d.paceState()
		}
	})
	if d.notify != nil {
		d.notify(ctx, d.source, why)
	}
}

// ---- one source ----

// historyWork is a chat the run imports, with what the selection reads of it (as of the step).
type historyWork struct {
	chat historyPlanChat
	id   string // the knowledge-base chat id
	need kb.ReadPlan
}

func (d *historyDriver) runSource(ctx context.Context, src assistantwire.KBHistorySource, info assistantwire.ArchiveSource) error {
	d.source, d.info, d.halted, d.pausedSince, d.lastOK = src.Name, info, nil, time.Time{}, false
	name := src.Name
	d.running.Store(&name)
	status := d.status(src.Name)
	// The pace goes on where it was: a rest or pause that is not over yet is waited out first, whatever stopped the
	// process meanwhile, and how slowly it reads, what it watches for and today's quiet periods carry over.
	now := d.now()
	d.pacer = newHistoryPacer(historyPaceFor(src), d.rng, d.zone, now, status.SlowUntil)
	d.pacer.slowPending = status.SlowPending
	if ps := status.Pace; ps != nil {
		d.pacer.restore(*ps, now)
		d.pausedSince = ps.PausedSince
	}
	d.log(historyLogLine{Event: "start", historyReason: reason("start", "profile", d.pacer.pace.Profile)})
	d.setStatus(src.Name, func(st *historySourceStatus) { st.State = "running"; st.Since = now.UTC() })
	if err := d.beat(true); err != nil {
		return err
	}
	if ps := status.Pace; ps != nil && ps.NotBefore.After(now) {
		state := ps.Waiting
		if state == "" {
			state = "resting"
		}
		d.log(historyLogLine{Event: "rest", historyReason: reason("carried_over", "state", state)})
		if err := d.wait(ctx, ps.NotBefore.Sub(now), state, historyReason{}); err != nil {
			return err
		}
	}
	if info.HistoryAnchors {
		if err := d.gatherAnchors(ctx); err != nil {
			return err
		}
	}
	p, _, err := loadHistoryPlan(d.data, src.Name)
	if err != nil || p == nil {
		return halt(false, "plan_gone")
	}
	e, err := d.eng(ctx)
	if err != nil {
		return halt(false, "kb_unavailable")
	}
	sel, ret := historySelection(e)
	var work []historyWork
	for _, r := range p.reading(sel, ret) {
		w := historyWork{chat: r.chat, id: kb.ChatID(r.chat.filing(src.Name), r.chat.ID), need: r.need}
		if historyChatDone(d.data, w.id, w.need) {
			continue
		}
		work = append(work, w)
	}
	d.rng.Shuffle(len(work), func(i, j int) { work[i], work[j] = work[j], work[i] })
	for i := 0; len(work) > 0; {
		i %= len(work)
		w := work[i]
		pages := d.pacer.switchPages()
		gone := false
		for n := 0; n < pages && !gone; n++ {
			// What is read comes from the selection as it is now: the owner may change it while the import runs.
			c, need, ok := d.chatNeed(ctx, w.chat.ID)
			if !ok || !need.Read {
				gone = true
				break
			}
			w.chat, w.need = c, need
			done, err := d.step(ctx, w)
			if err != nil {
				return err
			}
			gone = done
		}
		if gone {
			work = append(work[:i], work[i+1:]...)
			continue
		}
		i++
	}
	d.log(historyLogLine{Event: "done"})
	d.setStatus(src.Name, func(st *historySourceStatus) { st.State = "done" })
	return nil
}

// chatNeed is the plan's chat and what the selection reads of it now; false when the plan no longer has it.
func (d *historyDriver) chatNeed(ctx context.Context, id string) (historyPlanChat, kb.ReadPlan, bool) {
	p, ok, err := loadHistoryPlan(d.data, d.source)
	if err != nil || !ok {
		return historyPlanChat{}, kb.ReadPlan{}, false
	}
	c := p.chat(id)
	e, err := d.eng(ctx)
	if c == nil || err != nil {
		return historyPlanChat{}, kb.ReadPlan{}, false
	}
	sel, ret := historySelection(e)
	return *c, historyNeed(sel, ret, d.source, *c), true
}
