package assistant

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
)

// The assistant's tools for the history import (docs/assistant/knowledge.md "History import"): list a source's plan
// by what the selection reads of each chat and change the selection's per-item settings, see how far it got, stop it,
// approve and resume it. Listing a plan never starts anything: only kb_history_resume does, when the owner says to
// start.

// historyListed is how many chats each group of a listed plan shows (the rest are counted).
const (
	historyListedWhole    = 30
	historyListedPersonal = 30
	historyListedNone     = 20
	historyCloseNames     = 8 // names offered for one that matches no chat
)

// historySetArg is one change kb_history_plan makes to the selection's per-item settings.
type historySetArg struct {
	Chat  string `json:"chat"` // an id, or a chat's exact name
	Read  *bool  `json:"read"`
	Store bool   `json:"store"`
	Days  *int   `json:"days"`
	Reset bool   `json:"reset"` // remove the per-item setting: the rules decide again
}

func (s *Service) toolKBHistory(ctx context.Context, name string, args json.RawMessage) (string, error) {
	l := s.lang(ctx)
	e, err := s.kbGet(ctx)
	if err != nil {
		return "", err
	}
	data := e.Store.Dir
	set := s.kbSettings(ctx)
	var cfg *assistantwire.KBHistory
	if set != nil {
		cfg = set.History
	}
	switch name {
	case "kb_history_status":
		var a struct{ Source string }
		_ = json.Unmarshal(args, &a)
		return s.historyStatusText(ctx, l, e, cfg, a.Source), nil
	case "kb_history_stop":
		var a struct{ Reason string }
		_ = json.Unmarshal(args, &a)
		if e.Store.ReadOnly() {
			return "", kb.ErrReadOnly
		}
		return s.historyStop(ctx, l, data, a.Reason)
	}
	if e.Store.ReadOnly() {
		return "", kb.ErrReadOnly
	}
	if !cfg.On() {
		return i18n.T(l, "assistant-kb_history.off"), nil
	}
	switch name {
	case "kb_history_plan":
		var a struct {
			Source   string          `json:"source"`
			Set      []historySetArg `json:"set"`
			BackDays json.RawMessage `json:"back_days"`
		}
		if err := json.Unmarshal(args, &a); err != nil {
			return "", err
		}
		sources, msg := historySources(l, cfg, a.Source)
		if msg != "" {
			return msg, nil
		}
		if len(a.Set) > 0 || len(a.BackDays) > 0 {
			if len(sources) != 1 {
				return i18n.T(l, "assistant-kb_history.set_needs_source"), nil
			}
			return s.historySetItems(ctx, l, e, sources[0], a.Set, a.BackDays)
		}
		var b strings.Builder
		for i, src := range sources {
			if i > 0 {
				b.WriteString("\n")
			}
			b.WriteString(s.historyPlanText(ctx, l, e, src))
		}
		return b.String(), nil
	case "kb_history_resume":
		var a struct {
			Source string `json:"source"`
			Slow   bool   `json:"slow"`
		}
		_ = json.Unmarshal(args, &a)
		if a.Source == "" {
			return i18n.T(l, "assistant-kb_history.resume_needs_source"), nil
		}
		sources, msg := historySources(l, cfg, a.Source)
		if msg != "" {
			return msg, nil
		}
		return s.historyResume(ctx, l, e, sources[0], a.Slow)
	}
	return "", fmt.Errorf("no tool %q", name)
}

// historySources are the configured sources a call names (all of them for ""), or a message saying why none.
func historySources(l i18n.Lang, cfg *assistantwire.KBHistory, source string) ([]assistantwire.KBHistorySource, string) {
	if len(cfg.Sources) == 0 {
		return nil, i18n.T(l, "assistant-kb_history.no_sources")
	}
	if source == "" {
		return cfg.Sources, ""
	}
	if src, ok := cfg.Source(source); ok {
		return []assistantwire.KBHistorySource{src}, ""
	}
	var names []string
	for _, s := range cfg.Sources {
		names = append(names, s.Name)
	}
	return nil, i18n.T(l, "assistant-kb_history.not_listed", i18n.Args{"source": source, "sources": strings.Join(names, ", ")})
}

// historyBusyElsewhere: the import runs now, in this process or another (a fresh lease): the list is not refreshed
// then, so a tool's call never comes between the import's paced ones.
func (s *Service) historyBusyElsewhere(data string) bool {
	if s.kb.history.running.Load() != nil {
		return true
	}
	lease, ok := readHistoryLease(data)
	return ok && time.Since(lease.Heartbeat) < historyLeaseStale
}

// historyPlanText refreshes the source's plan from its list (unless the import runs or is stopped) and lists it by
// what the selection reads of each chat. An approval the selection now reads more than is taken back.
func (s *Service) historyPlanText(ctx context.Context, l i18n.Lang, e *kb.Engine, src assistantwire.KBHistorySource) string {
	data := e.Store.Dir
	if sel, _ := historySelection(e); sel.Stopgap() { // what it reads is not known now: nothing is listed or taken back
		return i18n.T(l, "assistant-kb_history.selection_unavailable")
	}
	var notes []string
	p, ok, err := loadHistoryPlan(data, src.Name)
	if err != nil {
		return i18n.T(l, "assistant-kb_history.plan_unreadable", i18n.Args{"source": src.Name})
	}
	if !ok {
		p = &historyPlan{Source: src.Name}
	}
	_, stopped := readHistoryStop(data)
	status := readHistoryStatus(data)[src.Name]
	refreshed := false
	switch {
	case stopped:
		notes = append(notes, i18n.T(l, "assistant-kb_history.refresh_stopped"))
	case s.historyBusyElsewhere(data):
		notes = append(notes, i18n.T(l, "assistant-kb_history.refresh_running"))
	case status.Pace != nil && status.Pace.NotBefore.After(time.Now()): // a rest or a pause the source asked for
		notes = append(notes, i18n.T(l, "assistant-kb_history.refresh_waiting", i18n.Args{"at": status.Pace.NotBefore.In(s.location(ctx)).Format("2006-01-02 15:04")}))
	default:
		err := s.historyRefresh(ctx, data, src.Name, p)
		var stop *historyHalt
		switch {
		case errors.Is(err, errHistoryUnavailable):
			return i18n.T(l, "assistant-kb_history.unavailable", i18n.Args{"source": src.Name})
		case errors.As(err, &stop):
			notes = append(notes, i18n.T(l, "assistant-kb_history.refresh_stopped_now", i18n.Args{"reason": historyReasonText(l, stop.why)}))
		case err != nil:
			notes = append(notes, i18n.T(l, "assistant-kb_history.refresh_failed", i18n.Args{"code": historyErrCode(err)}))
		default:
			refreshed = true
		}
	}
	sel, ret := historySelection(e)
	revoked := false
	if n, unlisted := p.widened(sel, ret); n > 0 || unlisted {
		p.revoke() // more to read than the owner saw: the owner sees it before anything runs
		notes = append(notes, i18n.T(l, "assistant-kb_history.plan_changed", i18n.Args{"count": max(n, 1)}))
		revoked = true
	}
	if refreshed || revoked {
		if err := saveHistoryPlan(data, p); err != nil {
			return i18n.T(l, "assistant-kb_history.plan_unreadable", i18n.Args{"source": src.Name})
		}
	}
	if len(p.Chats) == 0 && p.GeneratedAt.IsZero() {
		return strings.Join(append(notes, i18n.T(l, "assistant-kb_history.no_plan", i18n.Args{"source": src.Name})), "\n")
	}
	loc := s.location(ctx)
	var b strings.Builder
	b.WriteString(i18n.T(l, "assistant-kb_history.plan_header", i18n.Args{"source": src.Name, "chats": len(p.Chats),
		"generated": p.GeneratedAt.In(loc).Format("2006-01-02 15:04")}))
	b.WriteString("\n")
	if p.ApprovedAt != nil {
		b.WriteString(i18n.T(l, "assistant-kb_history.plan_approved", i18n.Args{"at": p.ApprovedAt.In(loc).Format("2006-01-02 15:04")}))
	} else {
		b.WriteString(i18n.T(l, "assistant-kb_history.plan_not_approved"))
	}
	b.WriteString("\n")
	if back := sel.BackDays(src.Name); back > 0 {
		b.WriteString(i18n.T(l, "assistant-kb_history.plan_back_days", i18n.Args{"days": back}) + "\n")
	} else {
		b.WriteString(i18n.T(l, "assistant-kb_history.plan_back_rules") + "\n")
	}
	for _, n := range notes {
		b.WriteString(n + "\n")
	}
	g := p.grouped(sel, ret)
	for _, group := range []struct {
		list []historyPlanChat
		key  string
		most int
	}{{g.whole, "group_whole", historyListedWhole}, {g.personal, "group_personal", historyListedPersonal}, {g.none, "group_none", historyListedNone}} {
		total := 0
		for _, c := range group.list {
			total += c.Estimate
		}
		b.WriteString("\n" + i18n.T(l, "assistant-kb_history."+group.key, i18n.Args{"count": len(group.list), "messages": total}) + "\n")
		for i, c := range group.list {
			if i == group.most {
				b.WriteString(i18n.T(l, "assistant-kb_history.more", i18n.Args{"count": len(group.list) - group.most}) + "\n")
				break
			}
			b.WriteString(historyChatLine(l, c, sel.Item(c.filing(src.Name), kb.ItemChat, c.ID)) + "\n")
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// historyChatLine is one chat of a listed plan, with its per-item setting when it has one.
func historyChatLine(l i18n.Lang, c historyPlanChat, it *kb.SelectionItem) string {
	kind := i18n.T(l, "assistant-kb_history.kind_other")
	switch c.Kind {
	case "dm", "group", "bot", "official", "system", historyUnlisted:
		kind = i18n.T(l, "assistant-kb_history.kind_"+c.Kind)
	}
	for _, f := range []struct {
		on  bool
		key string
	}{{c.Bot && c.Kind != "bot", "flag_bot"}, {c.Muted, "muted"}, {c.Collapsed, "flag_collapsed"}, {c.Done, "flag_done"}} {
		if f.on {
			kind += i18n.T(l, "assistant-kb_history."+f.key)
		}
	}
	name := c.Name
	if name == "" {
		name = "?"
	}
	if it == nil {
		return i18n.T(l, "assistant-kb_history.chat_line", i18n.Args{"name": name, "kind": kind, "estimate": c.Estimate, "id": c.ID})
	}
	return i18n.T(l, "assistant-kb_history.chat_line_set", i18n.Args{"name": name, "kind": kind, "estimate": c.Estimate, "id": c.ID,
		"setting": historyItemText(l, *it)})
}

// historyItemText says what a per-item setting does.
func historyItemText(l i18n.Lang, it kb.SelectionItem) string {
	switch {
	case !it.Read:
		return i18n.T(l, "assistant-kb_history.item_none")
	case !it.Store:
		return i18n.T(l, "assistant-kb_history.item_personal")
	case it.Days != nil:
		return i18n.T(l, "assistant-kb_history.item_whole_days", i18n.Args{"days": *it.Days})
	}
	return i18n.T(l, "assistant-kb_history.item_whole")
}

var errHistoryUnavailable = errors.New("the connector does not offer its history now")

// historyListChats asks the source for its chats (connectors.history_chats): one call, logged, never while another is
// in flight, and treated as the driver treats its own: the chat turn or request ending does not cancel it (only its
// own timeout does), and a timeout, a closed connection or a code it does not know stops the whole import (STOP, a
// push; the error is a *historyHalt) so that nothing, not even the next kb_history_plan, asks again until a person
// looked.
func (s *Service) historyListChats(ctx context.Context, data, source string) ([]connectorwire.HistoryChat, error) {
	d := s.historyDriver(data)
	d.source = source
	list, err := d.src.Archives(ctx)
	if err != nil {
		return nil, err
	}
	available := false
	for _, a := range list {
		available = available || a.ID == source && a.History
	}
	if !available {
		return nil, errHistoryUnavailable
	}
	key, err := historyLogKey(data)
	if err != nil {
		return nil, err
	}
	d.key, d.zone = key, s.location(ctx)
	d.calls.Lock()
	cctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), d.timeout)
	start := d.now()
	chats, err := d.src.Chats(cctx, source)
	took := d.now().Sub(start)
	if err != nil && errors.Is(cctx.Err(), context.DeadlineExceeded) {
		err = context.DeadlineExceeded
	}
	cancel()
	d.calls.Unlock()
	line := historyLogLine{Method: "history_chats", MS: took.Milliseconds(), Result: historyErrCode(err), N: len(chats)}
	d.log(line)
	line.At, line.Source = d.now().UTC(), source
	d.setStatus(source, func(st *historySourceStatus) { st.LastCall = &line; st.Calls++ })
	if took > historyVerySlow {
		why := reason("very_slow", "method", "history_chats", "ms", strconv.FormatInt(took.Milliseconds(), 10), "limit",
			strconv.FormatInt(historyVerySlow.Milliseconds(), 10), "minutes", "0")
		why.Code = "very_slow_list"
		d.log(historyLogLine{Event: "slow", historyReason: why})
		d.setStatus(source, func(st *historySourceStatus) { st.LastSlow = &historyEvent{At: d.now().UTC(), historyReason: why} })
	}
	if err != nil {
		if o, code := classify(err); o == historyStopIt {
			why := historyCall{method: "history_chats"}.stopWhy(code)
			d.stopAll(context.WithoutCancel(ctx), why)
			return nil, haltWhy(true, why)
		}
		return nil, err
	}
	return chats, nil
}

// historyRefresh lays the source's list over p (historyListChats), and takes the names it lists into the selection's
// per-item settings.
func (s *Service) historyRefresh(ctx context.Context, data, source string, p *historyPlan) error {
	chats, err := s.historyListChats(ctx, data, source)
	if err != nil {
		return err
	}
	p.Source = source
	p.regenerate(chats, time.Now())
	s.refreshSelectionNames(ctx, source, chats)
	return nil
}

// historyResolve finds the plan's chat a kb_history_plan change names: by id, else by its exact name. A name two chats
// have, or one no chat has, is a message instead (with the ids to choose from, or names close to it).
func historyResolve(l i18n.Lang, p *historyPlan, name string) (*historyPlanChat, string) {
	if c := p.chat(name); c != nil {
		return c, ""
	}
	var same []historyPlanChat
	for _, c := range p.Chats {
		if c.Name == name {
			same = append(same, c)
		}
	}
	switch len(same) {
	case 1:
		return &same[0], ""
	case 0:
		var close []string
		low := strings.ToLower(strings.TrimSpace(name))
		for _, c := range p.Chats {
			if low != "" && c.Name != "" && strings.Contains(strings.ToLower(c.Name), low) && len(close) < historyCloseNames {
				close = append(close, c.Name+" ("+c.ID+")")
			}
		}
		if len(close) == 0 {
			return nil, i18n.T(l, "assistant-kb_history.set_unknown", i18n.Args{"chat": name})
		}
		return nil, i18n.T(l, "assistant-kb_history.set_unknown_close", i18n.Args{"chat": name, "candidates": strings.Join(close, ", ")})
	}
	ids := make([]string, len(same))
	for i, c := range same {
		ids[i] = c.ID
	}
	return nil, i18n.T(l, "assistant-kb_history.set_ambiguous", i18n.Args{"chat": name, "count": len(same), "ids": strings.Join(ids, ", ")})
}

// historySetItems changes the selection's per-item settings of the plan's chats, and the source's back_days: all of
// them or, when one names no chat (or two) or is not valid, none. A change that reads more than the owner approved
// takes the approval back.
func (s *Service) historySetItems(ctx context.Context, l i18n.Lang, e *kb.Engine, src assistantwire.KBHistorySource, set []historySetArg,
	backRaw json.RawMessage) (string, error) {
	data := e.Store.Dir
	p, ok, err := loadHistoryPlan(data, src.Name)
	if err != nil {
		return "", err
	}
	if !ok {
		return i18n.T(l, "assistant-kb_history.no_plan", i18n.Args{"source": src.Name}), nil
	}
	type change struct {
		chat historyPlanChat
		arg  historySetArg
	}
	var changes []change
	for _, a := range set {
		c, msg := historyResolve(l, p, a.Chat)
		if msg != "" {
			return msg, nil
		}
		if !a.Reset && (a.Read == nil || !*a.Read && a.Store) {
			return i18n.T(l, "assistant-kb_history.set_invalid", i18n.Args{"chat": a.Chat}), nil
		}
		changes = append(changes, change{chat: *c, arg: a})
	}
	backSet, back := false, (*int)(nil)
	if len(backRaw) > 0 {
		if err := json.Unmarshal(backRaw, &back); err != nil {
			return i18n.T(l, "assistant-kb_history.back_days_invalid"), nil
		}
		backSet = true
	}
	_, err = s.updateKBSelection(ctx, func(sel *kb.Selection) (bool, error) {
		for _, c := range changes {
			filing := c.chat.filing(src.Name)
			if c.arg.Reset {
				sel.RemoveItem(filing, kb.ItemChat, c.chat.ID)
				continue
			}
			sel.SetItem(kb.SelectionItem{Source: filing, Kind: kb.ItemChat, ID: c.chat.ID, Name: c.chat.Name, Read: *c.arg.Read,
				Store: c.arg.Store, Days: c.arg.Days})
		}
		if backSet {
			if sel.History == nil {
				sel.History = map[string]kb.HistorySelection{}
			}
			sel.History[src.Name] = kb.HistorySelection{BackDays: back}
		}
		return true, sel.Validate()
	})
	if errors.Is(err, kb.ErrSelection) {
		return i18n.T(l, "assistant-kb_history.set_refused", i18n.Args{"detail": err.Error()}), nil
	}
	if err != nil { // the stored document does not read (or write) now: nothing changed
		s.logf("kb history: the selection was not changed: %v", err)
		return i18n.T(l, "assistant-kb_history.selection_unavailable"), nil
	}
	out := []string{}
	if len(changes) > 0 {
		out = append(out, i18n.T(l, "assistant-kb_history.set_done", i18n.Args{"count": len(changes)}))
	}
	if backSet {
		if back != nil {
			out = append(out, i18n.T(l, "assistant-kb_history.plan_back_days", i18n.Args{"days": *back}))
		} else {
			out = append(out, i18n.T(l, "assistant-kb_history.plan_back_rules"))
		}
	}
	// The engine reads the stored document: what it reads now decides whether the approval still holds.
	sel, ret := historySelection(e)
	if n, unlisted := p.widened(sel, ret); !sel.Stopgap() && (n > 0 || unlisted) {
		p.revoke()
		if err := saveHistoryPlan(data, p); err != nil {
			return "", err
		}
		out = append(out, i18n.T(l, "assistant-kb_history.set_revoked"))
	}
	return strings.Join(out, "\n"), nil
}

// historyStop writes STOP: the import stops before its next call (the one in flight finishes).
func (s *Service) historyStop(ctx context.Context, l i18n.Lang, data, words string) (string, error) {
	if st, stopped := readHistoryStop(data); stopped {
		return i18n.T(l, "assistant-kb_history.stop_already", i18n.Args{"reason": historyReasonText(l, st.historyReason)}), nil
	}
	// The owner's words stay in STOP and the status, where the owner reads them; the log gets only that it was asked
	// (they may name a chat or a person).
	why := reason("asked")
	if text := clipRunes(strings.TrimSpace(words), 200); text != "" {
		why.Text = text
	}
	now := time.Now().UTC()
	if err := writeHistoryStop(data, historyStop{At: now, historyReason: why, By: "tool"}); err != nil {
		return "", err
	}
	_ = appendHistoryLog(data, s.location(ctx), historyLogLine{At: now, Event: "stop", historyReason: why})
	if running := s.kb.history.running.Load(); running != nil {
		updateHistoryStatus(data, *running, func(st *historySourceStatus) { st.LastStop = &historyEvent{At: now, historyReason: why} })
	}
	return i18n.T(l, "assistant-kb_history.stop_done"), nil
}

// historyResume approves the source's plan as the selection reads it now, removes STOP and, with slow, keeps the
// slowest pace for two hours.
func (s *Service) historyResume(ctx context.Context, l i18n.Lang, e *kb.Engine, src assistantwire.KBHistorySource, slow bool) (string, error) {
	data := e.Store.Dir
	p, ok, err := loadHistoryPlan(data, src.Name)
	if err != nil {
		return "", err
	}
	if !ok || len(p.Chats) == 0 {
		return i18n.T(l, "assistant-kb_history.resume_no_plan", i18n.Args{"source": src.Name}), nil
	}
	sel, ret := historySelection(e)
	if sel.Stopgap() { // an approval records what the selection reads: not known now
		return i18n.T(l, "assistant-kb_history.selection_unavailable"), nil
	}
	now := time.Now().UTC()
	p.approve(sel, ret, now)
	if err := saveHistoryPlan(data, p); err != nil {
		return "", err
	}
	if err := removeHistoryStop(data); err != nil {
		return "", err
	}
	updateHistoryStatus(data, src.Name, func(st *historySourceStatus) {
		// slow: the slowest pace for two hours from the first call (it may come later: a quiet period, a rest).
		st.SlowUntil, st.SlowPending = time.Time{}, slow
		if st.Pace != nil { // a person looked: the two hours of pausing count from now; rests asked for still hold
			st.Pace.PausedSince = time.Time{}
		}
		if st.State == "stopped" {
			st.State = "idle"
		}
	})
	_ = appendHistoryLog(data, s.location(ctx), historyLogLine{At: now, Source: src.Name, Event: "resume",
		historyReason: reason("resume", "pace", map[bool]string{true: "slow", false: "normal"}[slow])})
	s.kbHistoryWake()
	key := "assistant-kb_history.resume_done"
	if slow {
		key = "assistant-kb_history.resume_done_slow"
	}
	out := i18n.T(l, key, i18n.Args{"source": src.Name})
	if src.SearchIndexNeeded() && !historySearchIndexed {
		out += "\n" + i18n.T(l, "assistant-kb_history.needs_index")
	}
	return out, nil
}

// historyStatusText is each configured source's state, progress, pace and last events.
func (s *Service) historyStatusText(ctx context.Context, l i18n.Lang, e *kb.Engine, cfg *assistantwire.KBHistory, only string) string {
	data := e.Store.Dir
	sel, ret := historySelection(e)
	switch {
	case cfg == nil:
		return i18n.T(l, "assistant-kb_history.off")
	case len(cfg.Sources) == 0 && !cfg.On():
		return i18n.T(l, "assistant-kb_history.off") + "\n" + i18n.T(l, "assistant-kb_history.no_sources")
	case len(cfg.Sources) == 0:
		return i18n.T(l, "assistant-kb_history.no_sources")
	}
	loc := s.location(ctx)
	at := func(t time.Time) string { return t.In(loc).Format("2006-01-02 15:04") }
	var b strings.Builder
	if !cfg.On() {
		b.WriteString(i18n.T(l, "assistant-kb_history.off") + "\n")
	}
	if st, stopped := readHistoryStop(data); stopped {
		b.WriteString(i18n.T(l, "assistant-kb_history.status_stop_file", i18n.Args{"reason": historyReasonText(l, st.historyReason), "at": at(st.At)}) + "\n")
	}
	if lease, ok := readHistoryLease(data); ok && lease.Instance != s.kb.history.instance && time.Since(lease.Heartbeat) < historyLeaseStale {
		b.WriteString(i18n.T(l, "assistant-kb_history.status_other_process", i18n.Args{"at": at(lease.Heartbeat)}) + "\n")
	}
	statuses := readHistoryStatus(data)
	speed := historySpeed(data, loc, time.Now())
	for _, src := range cfg.Sources {
		if only != "" && src.Name != only {
			continue
		}
		st := statuses[src.Name]
		d := &historyDriver{data: data, indexed: func() bool { return historySearchIndexed }}
		why, p := d.historyBlock(src, nil, sel, ret)
		state := st.State
		switch why {
		case "needs_search_index", "no_plan", "not_approved", "plan_unreadable", "selection_unavailable":
			state = why
		case "widened": // reads more than approved: it waits for the owner (the next round takes the approval back)
			state = "not_approved"
		case "done":
			state = "done"
		}
		if state == "" {
			state = "idle"
		}
		b.WriteString("\n" + i18n.T(l, "assistant-kb_history.status_header", i18n.Args{"source": src.Name,
			"state": i18n.T(l, "assistant-kb_history.state_"+historyStateKey(state))}) + "\n")
		if why == "needs_search_index" {
			b.WriteString(i18n.T(l, "assistant-kb_history.needs_index") + "\n")
		}
		if p != nil {
			if p.ApprovedAt != nil && why != "widened" {
				b.WriteString(i18n.T(l, "assistant-kb_history.plan_approved", i18n.Args{"at": at(*p.ApprovedAt)}) + "\n")
			} else {
				b.WriteString(i18n.T(l, "assistant-kb_history.plan_not_approved") + "\n")
			}
			b.WriteString(historyProgress(l, data, src.Name, p, sel, ret) + "\n")
		}
		if sp := speed[src.Name]; sp.calls > 0 {
			b.WriteString(i18n.T(l, "assistant-kb_history.status_speed", i18n.Args{"calls": sp.calls, "messages": sp.messages}) + "\n")
		}
		if !st.Until.IsZero() && (state == "resting" || state == "quiet" || state == "paused") {
			b.WriteString(i18n.T(l, "assistant-kb_history.status_until", i18n.Args{"at": at(st.Until)}) + "\n")
		}
		switch {
		case st.SlowPending:
			b.WriteString(i18n.T(l, "assistant-kb_history.status_slow_pending") + "\n")
		case st.SlowUntil.After(time.Now()):
			b.WriteString(i18n.T(l, "assistant-kb_history.status_slow_until", i18n.Args{"at": at(st.SlowUntil)}) + "\n")
		}
		for _, ev := range []struct {
			key string
			e   *historyEvent
		}{{"status_last_slow", st.LastSlow}, {"status_last_pause", st.LastPause}, {"status_last_stop", st.LastStop}} {
			if ev.e != nil {
				b.WriteString(i18n.T(l, "assistant-kb_history."+ev.key, i18n.Args{"reason": historyReasonText(l, ev.e.historyReason), "at": at(ev.e.At)}) + "\n")
			}
		}
		if c := st.LastCall; c != nil {
			key := "assistant-kb_history.status_last_call"
			if c.Chat != "" {
				key = "assistant-kb_history.status_last_call_chat"
			}
			b.WriteString(i18n.T(l, key, i18n.Args{"method": c.Method, "result": c.Result, "ms": c.MS, "at": at(c.At), "chat": c.Chat}) + "\n")
		}
	}
	return strings.TrimSpace(b.String())
}

func historyStateKey(state string) string {
	switch state {
	case "running", "resting", "quiet", "paused", "stopped", "done", "idle", "unavailable", "needs_search_index", "no_plan",
		"not_approved", "plan_unreadable", "selection_unavailable":
		return state
	}
	return "idle"
}

// historyProgress counts the chats the selection reads by state, with what was read and written, and names a few in
// error.
func historyProgress(l i18n.Lang, data, source string, p *historyPlan, sel *kb.Selection, ret kb.Retention) string {
	var done, todo, paused, failed, unreadable, read, written int
	var errs []string
	for _, r := range p.reading(sel, ret) {
		c := r.chat
		var hs kb.HistoryState
		ok, _ := readJSONFile(historyStatePathFor(data, kb.ChatID(c.filing(source), c.ID)), &hs)
		if ok {
			read += hs.Read
			written += hs.Written
		}
		switch {
		case !ok || hs.Finished() && !historyCovers(hs.Plan, r.need):
			todo++
		case hs.State == kb.HistoryDone:
			done++
		case hs.State == kb.HistoryError:
			failed++
			if len(errs) < 5 {
				errs = append(errs, orText(c.Name, c.ID)+" ("+hs.LastError+")")
			}
		case hs.State == kb.HistoryUnreadable:
			unreadable++
		case hs.State == kb.HistoryPaused:
			paused++
		default:
			todo++
		}
	}
	out := i18n.T(l, "assistant-kb_history.status_progress", i18n.Args{"done": done, "todo": todo, "paused": paused, "errors": failed,
		"unreadable": unreadable, "read": read, "written": written})
	if len(errs) > 0 {
		out += "\n" + i18n.T(l, "assistant-kb_history.status_errors", i18n.Args{"chats": strings.Join(errs, ", ")})
	}
	return out
}

type historyRate struct{ calls, messages int }

// historySpeed is each source's calls and messages read over the last hour, from the logs.
func historySpeed(data string, loc *time.Location, now time.Time) map[string]historyRate {
	out := map[string]historyRate{}
	since := now.Add(-time.Hour)
	for _, day := range []time.Time{since, now} {
		f, err := os.Open(filepath.Join(historyDir(data), "log-"+day.UTC().In(loc).Format("2006-01-02")+".jsonl"))
		if err != nil {
			continue
		}
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			var line historyLogLine
			if json.Unmarshal(sc.Bytes(), &line) != nil || line.Method == "" || line.At.Before(since) {
				continue
			}
			r := out[line.Source]
			r.calls++
			if line.Method == "history" {
				r.messages += line.N
			}
			out[line.Source] = r
		}
		f.Close()
		if since.In(loc).Format("2006-01-02") == now.In(loc).Format("2006-01-02") {
			break
		}
	}
	return out
}
