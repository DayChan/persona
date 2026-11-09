package assistant

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	mrand "math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The history import (docs/assistant/knowledge.md "History import"): a chat connector that can read its
// conversations backwards (connectors.history_chats / history / history_anchors, read-only) hands their older
// messages to the knowledge base, which merges them into the day files (kb.Engine.WriteHistory) as archive only.
//
// It runs in its own goroutine, never inside the knowledge base's loop, and keeps everything on disk under history/
// (kb_history_files.go), so a redeploy at any time loses nothing: a restart goes on from each chat's state. It reads
// a source only when all of these hold: the knowledge base is on and writable, kb.history.enabled is true and lists
// the source, the owner approved the source's plan in the assistant chat (kb_history_resume), there is no STOP, and
// the source does not wait for the search index (kb.SearchIndexed). One source at a time, in the order kb.history
// lists them; one request in flight, ever; every wait drawn afresh (kb_history_pace.go). A call that timed out or an
// answer it does not know stops the whole import (STOP, a push) until a person looks; nothing is retried by itself.
// A stop never cancels the call in flight: it takes effect before the next one.

const (
	kbHistoryPoll        = time.Minute     // how often the loop looks for something to run
	historyCallTimeout   = 3 * time.Minute // one call (the hub bounds it at two)
	historyWaitStep      = 30 * time.Second
	historyPauseMost     = 2 * time.Hour // paused longer than this without a call going through: stop
	historyTransientWait = 5 * time.Minute
)

// kbHistoryShared is the import's part of the Service.
type kbHistoryShared struct {
	calls    sync.Mutex // one request in flight to any source, the driver's and the tools' alike
	wake     chan struct{}
	running  atomic.Pointer[string] // the source this process is importing now
	instance string
}

// historySource is how the import reaches the connectors (through the hub; a fake in tests).
type historySource interface {
	Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error)
	Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error)
	Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error)
	Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error)
	Media(ctx context.Context, id, ref string) (connector.Media, error)
}

// historyDriver is one import: its dependencies, and the state of the run it is in.
type historyDriver struct {
	data      string
	src       historySource
	eng       func(context.Context) (*kb.Engine, error) // the knowledge base, open and writable
	cfg       func(context.Context) *assistantwire.KBHistory
	connected func() bool
	now       func() time.Time
	sleep     func(context.Context, time.Duration) error
	rng       *mrand.Rand
	logf      func(string, ...any)
	notify    func(ctx context.Context, source string, why historyReason)
	calls     *sync.Mutex
	instance  string
	indexed   func() bool
	running   *atomic.Pointer[string]
	timeout   time.Duration

	// The run.
	lock        *historyLock
	key         []byte
	zone        *time.Location
	source      string
	info        assistantwire.ArchiveSource
	pacer       *historyPacer
	pausedSince time.Time
	halted      error // a halt an image download met inside a page's write, acted on after it
	beatAt      time.Time
	// lastOK: the call before went through, with nothing but the interval since (no rest, pause or quiet period).
	lastOK bool
}

// historyHalt ends a run: stop (write STOP, push; a person must look) or not (the import was turned off, the
// knowledge base closed, STOP was there already: the loop looks again later).
type historyHalt struct {
	stop bool
	why  historyReason
}

func (h *historyHalt) Error() string { return h.why.String() }

// halt is a historyHalt for the reason code, with its arguments as key, value pairs (historyReason).
func halt(stop bool, code string, kv ...string) *historyHalt {
	return &historyHalt{stop: stop, why: reason(code, kv...)}
}

func haltWhy(stop bool, why historyReason) *historyHalt { return &historyHalt{stop: stop, why: why} }

// ---- the Service's side ----

func (s *Service) kbHistoryInit() {
	s.kb.history.wake = make(chan struct{}, 1)
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	s.kb.history.instance = hex.EncodeToString(b)
}

// kbHistoryWake has the loop look again now (a plan was approved, STOP removed).
func (s *Service) kbHistoryWake() {
	select {
	case s.kb.history.wake <- struct{}{}:
	default:
	}
}

// kbHistoryLoop runs the import while ctx lasts, looking every minute (at once when woken).
func (s *Service) kbHistoryLoop(ctx context.Context) {
	if s.kb.dir == "" {
		return
	}
	t := time.NewTicker(kbHistoryPoll)
	defer t.Stop()
	for {
		s.kbRun(ctx, func() { s.kbHistoryRound(ctx) })
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-s.kb.history.wake:
		}
	}
}

// kbHistoryRound runs what is runnable now, if anything: only while connected, not a mesh member's standby, the
// knowledge base writable and kb.history enabled.
func (s *Service) kbHistoryRound(ctx context.Context) {
	if !s.kbConnected() || s.hello(ctx).Assistant.Standby || !s.kbWritable(ctx) {
		return
	}
	if set := s.kbSettings(ctx); set == nil || !set.History.On() {
		return
	}
	e, err := s.kbGet(ctx)
	if err != nil || e.Store.ReadOnly() {
		return
	}
	d := s.historyDriver(e.Store.Dir)
	if err := d.runOnce(ctx); err != nil && !errors.Is(err, context.Canceled) {
		s.logf("kb history: %v", err)
	}
}

// historyDriver is an import of the knowledge base in data, reaching the connectors through the hub.
func (s *Service) historyDriver(data string) *historyDriver {
	return &historyDriver{
		data: data,
		src:  busHistory{s: s},
		eng: func(ctx context.Context) (*kb.Engine, error) {
			if !s.kbWritable(ctx) {
				return nil, errors.New("the knowledge base is not writable")
			}
			e, err := s.kbGet(ctx)
			if err == nil && e.Store.ReadOnly() {
				err = kb.ErrReadOnly
			}
			return e, err
		},
		cfg: func(ctx context.Context) *assistantwire.KBHistory {
			if set := s.kbSettings(ctx); set != nil && !set.Off && !set.ReadOnly {
				return set.History
			}
			return nil
		},
		connected: s.kbConnected,
		now:       time.Now,
		sleep:     sleepCtx,
		rng:       mrand.New(mrand.NewPCG(mrand.Uint64(), mrand.Uint64())),
		logf:      s.logf,
		notify: func(ctx context.Context, source string, why historyReason) {
			l := s.lang(ctx)
			title, body := "assistant-kb_history.stopped_title", "assistant-kb_history.stopped_body"
			if why.Code == "plan_changed" {
				title, body = "assistant-kb_history.plan_changed_title", "assistant-kb_history.plan_changed_body"
			}
			s.push(ctx, Notification{Subtitle: "Persona", Title: i18n.T(l, title),
				Body: i18n.T(l, body, i18n.Args{"source": source, "reason": historyReasonText(l, why), "count": why.Args["count"]})})
		},
		calls:    &s.kb.history.calls,
		instance: s.kb.history.instance,
		indexed:  func() bool { return historySearchIndexed },
		running:  &s.kb.history.running,
		timeout:  historyCallTimeout,
	}
}

// historySearchIndexed is kb.SearchIndexed (a var for the tests).
var historySearchIndexed = kb.SearchIndexed

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// busHistory reaches the connectors through the hub (connectors.*).
type busHistory struct{ s *Service }

func (b busHistory) Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error) {
	var list assistantwire.Archives
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchives, map[string]any{}, &list)
	return list.Archives, err
}

func (b busHistory) Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error) {
	var out assistantwire.HistoryChatsResult
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistoryChats, map[string]any{"id": id}, &out)
	return out.Chats, err
}

func (b busHistory) Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	var out connectorwire.HistoryPage
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistory, assistantwire.HistoryParams{ID: id, Query: q}, &out)
	return out, err
}

func (b busHistory) Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	var out connectorwire.HistoryAnchors
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsHistoryAnchors, assistantwire.HistoryAnchorsParams{ID: id, Query: q}, &out)
	return out, err
}

func (b busHistory) Media(ctx context.Context, id, ref string) (connector.Media, error) {
	var out struct {
		Media connector.Media `json:"media"`
	}
	err := b.s.appCall(ctx, AppConnectors, assistantwire.ConnectorsArchiveMedia, map[string]any{"id": id, "ref": ref}, &out)
	return out.Media, err
}

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

// ---- around every call ----

// gate is what is checked before every call and during every wait: the run ends when the process does, when STOP is
// there, when kb.history no longer enables the source, when the knowledge base is not writable, or when the plan is
// no longer approved, the selection reading more than was approved among the reasons (the approval is taken back).
func (d *historyDriver) gate(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, stopped := readHistoryStop(d.data); stopped {
		return halt(false, "stop_file")
	}
	cfg := d.cfg(ctx)
	if !cfg.On() {
		return halt(false, "disabled")
	}
	if _, ok := cfg.Source(d.source); !ok {
		return halt(false, "not_listed")
	}
	e, err := d.eng(ctx)
	if err != nil {
		return halt(false, "kb_unavailable")
	}
	if p, ok, err := loadHistoryPlan(d.data, d.source); err != nil || !ok || p.ApprovedAt == nil {
		return halt(false, "not_approved")
	}
	sel, ret := historySelection(e)
	if sel.Stopgap() { // the document does not read now: the approval is kept, and the next round tries again
		return halt(false, "selection_unavailable")
	}
	if n, err := d.revokeWidened(d.source, sel, ret); err != nil {
		return halt(true, "plan_save_failed", "class", errClass(err))
	} else if n > 0 {
		return halt(false, "plan_changed", "count", strconv.Itoa(n))
	}
	return d.beat(false)
}

// beat renews the lease's heartbeat, at most once a minute unless forced.
func (d *historyDriver) beat(force bool) error {
	if d.lock == nil {
		return nil
	}
	now := d.now()
	if !force && now.Sub(d.beatAt) < time.Minute {
		return nil
	}
	d.beatAt = now
	return d.lock.beat(now, d.source)
}

// wait waits dur in steps, going through the gate at each: a stop, or the import turned off, ends it early. A wait
// with a state (a rest, a pause, a quiet period) is kept in status.json, so a restart waits out what is left of it.
func (d *historyDriver) wait(ctx context.Context, dur time.Duration, state string, why historyReason) error {
	if dur <= 0 {
		return d.gate(ctx)
	}
	end := d.now().Add(dur)
	if state != "" {
		d.lastOK = false
		d.pacer.notBefore, d.pacer.waiting = end, state
		d.setStatus(d.source, func(st *historySourceStatus) {
			st.State = state
			st.Until = end.UTC()
			if state == "paused" && why.Code != "" {
				st.LastPause = &historyEvent{At: d.now().UTC(), historyReason: why}
			}
			st.Pace = d.paceState()
		})
	}
	for {
		if err := d.gate(ctx); err != nil {
			return err
		}
		left := end.Sub(d.now())
		if left <= 0 {
			break
		}
		if err := d.sleep(ctx, min(left, historyWaitStep)); err != nil {
			return err
		}
	}
	if state != "" {
		d.pacer.notBefore, d.pacer.waiting = time.Time{}, ""
		d.setStatus(d.source, func(st *historySourceStatus) { st.State = "running"; st.Until = time.Time{}; st.Pace = d.paceState() })
	}
	return nil
}

// paced waits what the pace says before the next call: a quiet period, a rest that is due, then the interval.
func (d *historyDriver) paced(ctx context.Context, image bool) error {
	if end := d.pacer.quietUntil(d.now()); !end.IsZero() {
		d.log(historyLogLine{Event: "rest", historyReason: reason("quiet")})
		if err := d.wait(ctx, end.Sub(d.now()), "quiet", historyReason{}); err != nil {
			return err
		}
	}
	if rest, why := d.pacer.rest(d.now()); rest > 0 {
		d.log(historyLogLine{Event: "rest", historyReason: reason(why, "minutes", strconv.Itoa(int(rest.Minutes())))})
		if err := d.wait(ctx, rest, "resting", historyReason{}); err != nil {
			return err
		}
	}
	return d.wait(ctx, d.pacer.interval(image, d.now()), "", historyReason{})
}

// historyCall is what a call came to: the call's own error (nil: an answer), and what its duration asks for after
// the answer is handled (a rest before the next call, or a stop).
type historyCall struct {
	err    error
	rest   time.Duration
	stop   *historyHalt
	method string
	chat   string // its hash
	prevOK bool   // the call before it went through, with only the interval between (lastOK)
}

// classify is what the call's error means. A source that answers not ready right after a call of ours went through
// is taken as made unready by it (docs/assistant/knowledge.md "Stops"): that stops, where not ready otherwise
// pauses.
func (c historyCall) classify() (historyOutcome, string) {
	o, code := classify(c.err)
	if code == "not_ready" && c.prevOK {
		return historyStopIt, code
	}
	return o, code
}

// stopWhy is the reason a call's stop-class error stops the import for.
func (c historyCall) stopWhy(code string) historyReason {
	r := reason("call_unknown_code", "code", code, "method", c.method, "chat", c.chat)
	switch {
	case code == "not_ready":
		r.Code = "not_ready_after_call"
	case historyTimeoutCode(code):
		r.Code = "call_timeout"
	case code == "connection_closed":
		r.Code = "call_connection_closed"
	case code == "error":
		r.Code = "call_no_code"
	}
	return r
}

// call makes one call of method for chat (its id, hashed in the log) after the pace's wait: one in flight, never
// cancelled by a stop (its context only ends on its own timeout), logged, timed and judged. f returns how many items
// the answer had, its result and its pace for the log. An error from call itself means no call was made: the gate or
// the pace's wait ended the run.
func (d *historyDriver) call(ctx context.Context, method, filing, chat string, image bool, f func(context.Context) (n int, result, pace string, err error)) (historyCall, error) {
	if err := d.paced(ctx, image); err != nil {
		return historyCall{}, err
	}
	d.calls.Lock()
	if !d.connected() {
		d.calls.Unlock()
		return historyCall{}, halt(false, "not_connected")
	}
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.timeout)
	start := d.now()
	if d.pacer.slowPending { // kb_history_resume with slow: the two slow hours start with the first call
		d.pacer.slowPending, d.pacer.slowUntil = false, start.Add(historySlowStart)
		d.pacer.mult = max(d.pacer.mult, d.pacer.maxMult())
		d.setStatus(d.source, func(st *historySourceStatus) { st.SlowPending, st.SlowUntil = false, d.pacer.slowUntil.UTC() })
	}
	n, result, pace, err := f(cctx)
	took := d.now().Sub(start)
	if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}
	cancel()
	d.calls.Unlock()
	if err != nil {
		result = historyErrCode(err)
	}
	hash := historyHash(d.key, filing, chat)
	line := historyLogLine{Method: method, Chat: hash, MS: took.Milliseconds(), Result: result, N: n, Pace: pace}
	d.log(line)
	line.At, line.Source = d.now().UTC(), d.source
	out := historyCall{err: err, method: method, chat: hash, prevOK: d.lastOK}
	d.lastOK = err == nil && pace == ""
	if d.lastOK {
		d.pausedSince = time.Time{}
	}
	v := d.pacer.observe(method, took, d.now())
	out.rest = v.rest
	if v.stop.Code != "" {
		out.stop = haltWhy(true, v.stop)
	}
	d.setStatus(d.source, func(st *historySourceStatus) {
		st.LastCall = &line
		st.Calls++
		if v.note.Code != "" {
			st.LastSlow = &historyEvent{At: d.now().UTC(), historyReason: v.note}
		}
		st.Pace = d.paceState()
	})
	if v.note.Code != "" {
		d.log(historyLogLine{Event: "slow", historyReason: v.note})
	}
	return out, nil
}

// after does what a call's duration asked for, once its answer is handled: stop, or rest.
func (d *historyDriver) after(ctx context.Context, c historyCall) error {
	if c.stop != nil {
		return c.stop
	}
	if c.rest > 0 {
		d.log(historyLogLine{Event: "rest", historyReason: reason("slow_rest", "minutes", strconv.Itoa(int(c.rest.Minutes())))})
		return d.wait(ctx, c.rest, "resting", historyReason{})
	}
	return nil
}

// pause waits after a pace hint or a transient error, then the same read goes again; paused longer than two hours
// in a row (nothing went through since), it stops instead.
func (d *historyDriver) pause(ctx context.Context, dur time.Duration, why historyReason) error {
	now := d.now()
	if d.pausedSince.IsZero() {
		d.pausedSince = now
	}
	if now.Add(dur).Sub(d.pausedSince) > historyPauseMost {
		return halt(true, "pause_too_long", "hours", strconv.Itoa(int(historyPauseMost.Hours())), "code", why.Args["code"])
	}
	d.log(historyLogLine{Event: "pause", historyReason: why})
	return d.wait(ctx, dur, "paused", why)
}

// pauseFor is the reason of a pause for code: a source's retry_after (with how long it asked) or a transient code.
func pauseFor(code string) historyReason {
	if ms, ok := strings.CutPrefix(code, "retry_after="); ok {
		n, _ := strconv.ParseInt(ms, 10, 64)
		return reason("source_pace", "code", "retry_after", "seconds", strconv.FormatInt(n/1000, 10))
	}
	return reason("transient", "code", code)
}

// ---- errors ----

// What an answer or an error means for the import.
type historyOutcome int

const (
	historyNone       historyOutcome = iota // (the zero value: nothing to act on)
	historyPauseIt                          // wait, then read the same again
	historyStopIt                           // stop the whole import for a person
	historyChatError                        // this chat cannot be read as asked: error, permanent
	historyUnreadable                       // this chat cannot be read any more: skipped, not a failure
)

// Codes a source answers that pass by themselves (the import waits and reads the same again).
var historyTransient = map[string]bool{"busy": true, "not_ready": true, "rate_limited": true, "too_many_requests": true}

// Codes that say this one chat cannot be read: gone (unreadable, as a page with unreadable says) or refused.
var historyGone = map[string]bool{"not_found": true, "gone": true, "conversation_not_found": true}
var historyRefused = map[string]bool{"forbidden": true, "permission_denied": true, "no_permission": true, "not_member": true,
	"conversation_not_member": true}

// historyErrCode is an error's code for the log and the status: never its message, which may name a chat.
func historyErrCode(err error) string {
	var re *rpc.Error
	switch {
	case err == nil:
		return "ok"
	case errors.Is(err, context.DeadlineExceeded):
		return "timeout"
	case isHistoryQueryErr(err):
		return "query"
	case errors.As(err, &re) && re.Code != "":
		return normCode(re.Code)
	case errors.Is(err, rpc.ErrClosed):
		return "connection_closed"
	}
	return "error"
}

func normCode(c string) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimSpace(c)), "-", "_")
}

// errClass is a short word for an error of the import's own (a file), for the log.
func errClass(err error) string {
	switch {
	case errors.Is(err, os.ErrPermission):
		return "permission"
	case errors.Is(err, os.ErrNotExist):
		return "not_found"
	}
	return "error"
}

func isHistoryQueryErr(err error) bool {
	return errors.Is(err, connectorwire.ErrQuery) || strings.HasPrefix(err.Error(), connectorwire.ErrQuery.Error()+":")
}

// classify says what err means, with its code. A timeout, a closed connection (the result is unknown, as for a
// timeout) and any code it does not know stop the import: a person looks, nothing is retried by itself.
func classify(err error) (historyOutcome, string) {
	code := historyErrCode(err)
	switch {
	case code == "query":
		return historyChatError, code
	case historyTimeoutCode(code) || code == "connection_closed":
		return historyStopIt, code
	case historyTransient[code]:
		return historyPauseIt, code
	case historyGone[code]:
		return historyUnreadable, code
	case historyRefused[code]:
		return historyChatError, code
	}
	return historyStopIt, code
}

func historyTimeoutCode(code string) bool {
	return strings.Contains(code, "timeout") || strings.Contains(code, "timed_out")
}

// ---- the log and the status ----

func (d *historyDriver) log(l historyLogLine) {
	if l.At.IsZero() {
		l.At = d.now()
	}
	if l.Source == "" {
		l.Source = d.source
	}
	l.historyReason = l.historyReason.logged()
	if err := appendHistoryLog(d.data, d.zone, l); err != nil {
		d.logf("kb history: log: %v", err)
	}
}

// historySourceStatus is a source's line of history/status.json.
type historySourceStatus struct {
	State     string          `json:"state,omitempty"` // running, resting, quiet, paused, stopped, done, idle, unavailable
	Since     time.Time       `json:"since,omitzero"`
	Until     time.Time       `json:"until,omitzero"` // the end of a rest, quiet period or pause
	Calls     int             `json:"calls,omitempty"`
	LastCall  *historyLogLine `json:"last_call,omitempty"`
	LastSlow  *historyEvent   `json:"last_slow,omitempty"`
	LastPause *historyEvent   `json:"last_pause,omitempty"`
	LastStop  *historyEvent   `json:"last_stop,omitempty"`
	// SlowUntil: kb_history_resume with slow: the slowest pace until then. SlowPending: asked for, and the two hours
	// start with the next call (the first after the resume).
	SlowUntil   time.Time `json:"slow_until,omitzero"`
	SlowPending bool      `json:"slow_pending,omitempty"`
	// Pace is the pacer's state, so a restart goes on at the same pace (kb_history_pace.go).
	Pace *historyPaceState `json:"pace,omitempty"`
}

type historyEvent struct {
	At time.Time `json:"at"`
	historyReason
}

// paceState is the pacer's state with the pause the driver counts, for status.json.
func (d *historyDriver) paceState() *historyPaceState {
	if d.pacer == nil {
		return nil
	}
	ps := d.pacer.snapshot()
	ps.PausedSince = d.pausedSince
	return &ps
}

func historyStatusPath(data string) string { return filepath.Join(historyDir(data), "status.json") }

var historyStatusMu sync.Mutex

func readHistoryStatus(data string) map[string]historySourceStatus {
	out := map[string]historySourceStatus{}
	_, _ = readJSONFile(historyStatusPath(data), &out)
	return out
}

func updateHistoryStatus(data, source string, f func(*historySourceStatus)) {
	historyStatusMu.Lock()
	defer historyStatusMu.Unlock()
	all := readHistoryStatus(data)
	st := all[source]
	f(&st)
	all[source] = st
	_ = writeJSONFile(historyStatusPath(data), all)
}

func (d *historyDriver) status(source string) historySourceStatus {
	return readHistoryStatus(d.data)[source]
}

func (d *historyDriver) setStatus(source string, f func(*historySourceStatus)) {
	if source == "" {
		return
	}
	updateHistoryStatus(d.data, source, f)
}

func orText(s, d string) string {
	if s == "" {
		return d
	}
	return s
}
