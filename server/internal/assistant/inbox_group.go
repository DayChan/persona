package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Inbox events (#1680, docs/assistant/inbox.md "Event grouping"). A connector that declares group hands a conversation's new messages
// over as a batch (InboxThing.Group) instead of an item. The hub asks the model which events — inbox items of that
// conversation — the messages belong to: it reads the events (the events tool, its own) and the conversation (the
// connector's tools, group.tool through the hub), at most groupCalls calls, and answers with a title, a summary and a tier for every
// event it touches. The connector writes each event (group.append: one event a call, its meta in and out; the hub never
// reads meta) and the hub stores what comes back with the model's words, triaged_by "model". When the model can't (not
// set up, held, failing, too slow, or an answer that does not hold up) the connector groups the batch by structure
// itself (group.fallback) and the events keep its rule tiers, "By rule", triaged_by "rules": that version is final.

const (
	groupCalls       = 4                  // tool calls one batch may make
	groupNewMax      = 3                  // new events the model may open for one batch
	groupEventsMax   = 10                 // events the events tool lists
	groupDoneFor     = 3 * 24 * time.Hour // how long a done event is still listed
	groupConcurrency = 4                  // batches related at once (the others wait for the next round)
	groupAttempts    = 3                  // tries of one event's append
)

// Bounds, variables for tests.
var (
	groupTimeout     = 2 * time.Minute  // the model's part of one batch, its tool calls included
	groupStepTimeout = 45 * time.Second // one model call
	groupCallTimeout = 30 * time.Second // one call to the connector (a tool, the fallback, an append)
	groupRetryWait   = time.Second      // before an append is tried again (doubling)
)

// InboxGroup is an event's place: the connector that writes it and the conversation (thread) it is in.
type InboxGroup struct {
	Connector string `json:"connector"`
	Thread    string `json:"thread"`
}

// inboxGrouping is the batches being related: one conversation at a time (two batches of one conversation would
// write the same event's meta over each other), a batch handed again meanwhile is left alone.
type inboxGrouping struct {
	mu      sync.Mutex
	running map[string]string // connector id + thread → the batch being related
	why     map[string]string // connector id → why its last batch went by rules ("" by the model): logged on change
	wg      sync.WaitGroup
}

var undeclaredGroups sync.Map

// relateBatches starts relating c's batches (things with Group) in the background and returns its other things.
// Batches past groupConcurrency, and those of a conversation being related, wait for the next round: the connector
// hands them again until each of their messages is in an event.
func (s *Service) relateBatches(ctx context.Context, c assistantwire.InboxSource, things []connector.InboxThing) []connector.InboxThing {
	rest := things[:0:0]
	var batches []connector.InboxThing
	for _, t := range things {
		if t.Group == nil {
			rest = append(rest, t)
		} else {
			batches = append(batches, t)
		}
	}
	if len(batches) == 0 {
		return rest
	}
	if !c.Group {
		if _, logged := undeclaredGroups.LoadOrStore(c.ID, true); !logged {
			s.logf("inbox: %s: hands batches to relate but does not declare group: they are dropped", c.ID)
		}
		return rest
	}
	ctx = context.WithoutCancel(ctx) // the round (a pull to refresh) does not wait for them
	gr := &s.inbox.grouping
	for _, t := range batches {
		if t.Thread == "" || t.Group.Batch == "" || len(t.Group.New) == 0 {
			continue
		}
		key := c.ID + "\x00" + t.Thread
		gr.mu.Lock()
		if gr.running == nil {
			gr.running = map[string]string{}
		}
		if _, busy := gr.running[key]; busy || len(gr.running) >= groupConcurrency {
			gr.mu.Unlock()
			continue
		}
		gr.running[key] = t.Group.Batch
		gr.wg.Add(1)
		gr.mu.Unlock()
		go func() {
			defer func() {
				gr.mu.Lock()
				delete(gr.running, key)
				gr.mu.Unlock()
				gr.wg.Done()
			}()
			s.relateBatch(ctx, c, t)
		}()
	}
	return rest
}

// groupEvent is an event as the events tool lists it, and as the model's answer may name it.
type groupEvent struct {
	ID      string          `json:"id"`
	Title   string          `json:"title"`
	Summary string          `json:"summary,omitempty"`
	Tier    string          `json:"tier,omitempty"`
	Version string          `json:"version"`
	Done    bool            `json:"done"`
	Meta    json.RawMessage `json:"meta"`
}

// groupPlan is which messages go to which event, and what each event is: the model's, or the connector's fallback.
type groupPlan struct {
	model  bool
	events []plannedEvent
}

// plannedEvent is one event a batch touches: an existing one or a new one (which may continue a done one).
type plannedEvent struct {
	existing  string // the event's id; "" a new one
	messages  []string
	continues string
	// The model's words for it; under the fallback only a new one's title and rule tier (the connector's).
	title, summary, tier, reason string
	urgent                       bool
}

// relateBatch relates one batch: by the model, else by the connector's fallback; then has each event written.
func (s *Service) relateBatch(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing) {
	events := s.groupEvents(c.ID, t.Thread, "")
	plan, why := s.askGroup(ctx, c, t, events)
	if plan == nil {
		var err error
		if plan, err = s.fallbackGroup(ctx, c, t, events); err != nil {
			if errors.Is(err, errNoFallback) { // the batch waits for the model, as an inbox item without a fallback does
				s.noteGroupWhy(c.ID, why+"; no fallback: waiting for the model")
				return
			}
			s.logf("inbox: %s: batch %s: not related (%s), and its fallback failed: %v", c.ID, t.Group.Batch, why, err)
			return
		}
	}
	s.noteGroupWhy(c.ID, why)
	s.writeGroup(ctx, c, t, plan)
}

// errNoFallback: the connector has no group.fallback (it answered an unknown method).
var errNoFallback = errors.New("no group.fallback")

// unsupported says whether a connector answered that it has no such method.
func unsupported(err error) bool {
	var re *rpc.Error
	if errors.As(err, &re) {
		switch re.Code {
		case "unknown_method", "unsupported", "no_capability":
			return true
		}
	}
	return false
}

// noteGroupWhy logs when a connector's batches start going by rules (or why changes), and when the model is back.
func (s *Service) noteGroupWhy(id, why string) {
	gr := &s.inbox.grouping
	gr.mu.Lock()
	if gr.why == nil {
		gr.why = map[string]string{}
	}
	prev, seen := gr.why[id]
	gr.why[id] = why
	gr.mu.Unlock()
	switch {
	case why != "" && why != prev:
		s.logf("inbox: %s: relating by the connector's rules: %s", id, why)
	case why == "" && seen && prev != "":
		s.logf("inbox: %s: relating by the model again", id)
	}
}

// groupEvents are the events of a conversation as the events tool answers: the open ones, then those done within
// groupDoneFor, newest first, at most groupEventsMax; query keeps those whose title, summary or meta has it.
func (s *Service) groupEvents(connectorID, thread, query string) []groupEvent {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	cutoff := time.Now().Add(-groupDoneFor)
	q := strings.ToLower(strings.TrimSpace(query))
	var list []*InboxItem
	for _, it := range b.items {
		if it.Group == nil || it.Group.Connector != connectorID || !sameConversation(it.Group.Thread, thread) ||
			!it.open() && it.DoneAt.Before(cutoff) {
			continue
		}
		if q != "" && !strings.Contains(strings.ToLower(it.Title+"\n"+it.Summary+"\n"+string(it.Meta)), q) {
			continue
		}
		list = append(list, it)
	}
	sort.Slice(list, func(i, j int) bool {
		if list[i].open() != list[j].open() {
			return list[i].open()
		}
		return list[i].UpdatedAt.After(list[j].UpdatedAt)
	})
	out := make([]groupEvent, 0, min(len(list), groupEventsMax))
	for _, it := range list[:min(len(list), groupEventsMax)] {
		out = append(out, groupEvent{ID: it.ID, Title: it.Title, Summary: it.Summary, Tier: it.Tier, Version: inboxVersion(it),
			Done: !it.open(), Meta: it.Meta})
	}
	return out
}

// sameConversation: an event's thread is the conversation's, or one under it (the conversation's thread followed by
// a separator, not more of its id).
func sameConversation(event, thread string) bool {
	if event == thread {
		return true
	}
	if !strings.HasPrefix(event, thread) {
		return false
	}
	c := event[len(thread)]
	return !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_' || c == '-')
}

// groupAnswer is one model reply: a tool call, or the answer.
type groupAnswer struct {
	Tool   string          `json:"tool"`
	Args   json.RawMessage `json:"args"`
	Assign []struct {
		Message string `json:"message"`
		Event   string `json:"event"`
	} `json:"assign"`
	Events []struct {
		Event     string `json:"event"`
		Title     string `json:"title"`
		Summary   string `json:"summary"`
		Tier      string `json:"tier"`
		Reason    string `json:"reason"`
		Urgent    bool   `json:"urgent"`
		Continues string `json:"continues"`
	} `json:"events"`
}

// askGroup has the model relate the batch, calling tools at most groupCalls times, within groupTimeout. nil and why
// when the model can't or its answer does not hold up (the fallback then groups it).
func (s *Service) askGroup(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing, events []groupEvent) (*groupPlan, string) {
	ctx, cancel := context.WithTimeout(ctx, groupTimeout)
	defer cancel()
	rules := strings.TrimSpace(t.Rules)
	if rules == "" {
		rules = s.inboxConfig(ctx).Rules
	}
	hello := s.hello(ctx)
	system := prompts.RenderInboxGroup(prompts.InboxGroup{Owner: hello.Owner, MetaDescription: strings.TrimSpace(c.MetaDescription),
		Rules: rules, MaxCalls: groupCalls, NewMax: groupNewMax, Language: answerLanguage(hello.Locale)})
	seen := map[string]groupEvent{} // every event the model was shown: the only ones its answer may name
	for _, e := range events {
		seen[e.ID] = e
	}
	in := prompts.InboxGroupInput{Thread: t.Thread, New: t.Group.New, Context: t.Context, Events: jsonText(events)}
	for {
		in.Left = groupCalls - len(in.Calls)
		var out groupAnswer
		sctx, scancel := context.WithTimeout(ctx, groupStepTimeout)
		err := s.inboxModelJSON(sctx, purposeTriage, system, prompts.RenderInboxGroupInput(in), 0, &out)
		scancel()
		paused, _ := modelPaused(err)
		switch {
		case modelUnconfigured(err):
			return nil, "没有配置分拣模型"
		case paused:
			return nil, "模型暂时不可用：" + err.Error()
		case err != nil:
			return nil, "模型出错：" + err.Error()
		case out.Tool == "":
			plan, err := checkGroupAnswer(out, t.Group.New, seen)
			if err != nil {
				return nil, "模型的结果不合格：" + err.Error()
			}
			return plan, ""
		case in.Left == 0:
			return nil, fmt.Sprintf("模型查了 %d 次还要查", groupCalls)
		}
		result := s.groupTool(ctx, c.ID, t.Thread, out.Tool, out.Args, seen)
		in.Calls = append(in.Calls, prompts.InboxGroupCall{Tool: out.Tool, Args: argsText(out.Args), Result: result})
	}
}

// groupTool runs one tool for the model: events here, the conversation's through the connector (at the hub). A
// failure is the tool's answer ({"error": …}), the call counted.
func (s *Service) groupTool(ctx context.Context, connectorID, thread, name string, args json.RawMessage, seen map[string]groupEvent) string {
	switch name {
	case "events":
		var a struct {
			Query string `json:"query"`
		}
		_ = json.Unmarshal(args, &a)
		events := s.groupEvents(connectorID, thread, a.Query)
		for _, e := range events {
			seen[e.ID] = e
		}
		return jsonText(events)
	case connector.GroupMessagesBefore, connector.GroupMessagesAround, connector.GroupSearch:
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage(`{}`)
		}
		cctx, cancel := context.WithTimeout(ctx, groupCallTimeout)
		defer cancel()
		var res struct {
			Result string `json:"result"`
		}
		err := s.appCall(cctx, AppInbox, inboxGroupTool, map[string]any{"source": connectorID,
			"call": connector.GroupToolCall{Thread: thread, Name: name, Args: args}}, &res)
		if unsupported(err) {
			return jsonText(map[string]string{"error": "这个工具用不了：连接器没有它，别再调，用已有的信息判断"})
		}
		if err != nil {
			return jsonText(map[string]string{"error": err.Error()})
		}
		return res.Result
	}
	return jsonText(map[string]string{"error": "没有这个工具：" + name})
}

// checkGroupAnswer holds the model's answer to the rules: every new message in exactly one event; an existing event
// only one it was shown and not done; at most groupNewMax new ones (new:<n>), a done one they continue one it was
// shown; every event used described, with a known tier and a title.
func checkGroupAnswer(out groupAnswer, fresh []string, seen map[string]groupEvent) (*groupPlan, error) {
	want := map[string]bool{}
	for _, m := range fresh {
		want[m] = true
	}
	byEvent := map[string][]string{}
	var order []string
	for _, a := range out.Assign {
		if !want[a.Message] {
			return nil, fmt.Errorf("消息 %q 不是这一批的，或者归了两次", a.Message)
		}
		delete(want, a.Message)
		if _, ok := byEvent[a.Event]; !ok {
			order = append(order, a.Event)
		}
		byEvent[a.Event] = append(byEvent[a.Event], a.Message)
	}
	if len(want) > 0 {
		var left []string
		for _, m := range fresh {
			if want[m] {
				left = append(left, m)
			}
		}
		return nil, fmt.Errorf("消息 %s 没有归到事件", strings.Join(left, "、"))
	}
	described := map[string]int{}
	for i, e := range out.Events {
		if _, twice := described[e.Event]; twice {
			return nil, fmt.Errorf("事件 %q 写了两次", e.Event)
		}
		described[e.Event] = i
	}
	plan := &groupPlan{model: true}
	news := 0
	for _, ev := range order {
		i, ok := described[ev]
		if !ok {
			return nil, fmt.Errorf("事件 %q 没有标题和分级", ev)
		}
		d := out.Events[i]
		if !connector.Tier(d.Tier).Known() {
			return nil, fmt.Errorf("事件 %q 的分级 %q 不对", ev, d.Tier)
		}
		if strings.TrimSpace(d.Title) == "" {
			return nil, fmt.Errorf("事件 %q 没有标题", ev)
		}
		pe := plannedEvent{messages: byEvent[ev], title: d.Title, summary: d.Summary, tier: d.Tier, reason: d.Reason,
			urgent: d.Urgent && d.Tier == TierAction}
		if isNewEvent(ev) {
			news++
			if d.Continues != "" {
				if s, ok := seen[d.Continues]; !ok || !s.Done {
					return nil, fmt.Errorf("事件 %q 接着的 %q 不是查到的已处理的事", ev, d.Continues)
				}
				pe.continues = d.Continues
			}
		} else {
			s, ok := seen[ev]
			switch {
			case !ok:
				return nil, fmt.Errorf("事件 %q 不是查到的事", ev)
			case s.Done:
				return nil, fmt.Errorf("事件 %q 已经处理完，不能再往里加", ev)
			}
			pe.existing = ev
		}
		plan.events = append(plan.events, pe)
	}
	if news > groupNewMax {
		return nil, fmt.Errorf("新开了 %d 件事，最多 %d 件", news, groupNewMax)
	}
	return plan, nil
}

// isNewEvent: new:<n>, the model's name for an event it opens.
func isNewEvent(ev string) bool {
	n, ok := strings.CutPrefix(ev, "new:")
	if !ok || n == "" {
		return false
	}
	_, err := strconv.ParseUint(n, 10, 32)
	return err == nil
}

// fallbackGroup has the connector group the batch by structure, with the same events the model would have seen,
// and holds its answer to the same rules (each new message once; an existing event a listed one, not done).
func (s *Service) fallbackGroup(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing, events []groupEvent) (*groupPlan, error) {
	listed := map[string]bool{}
	in := connector.GroupFallback{Thread: t.Thread, Batch: t.Group.Batch, Events: []connector.GroupEvent{}}
	for _, e := range events {
		listed[e.ID] = !e.Done
		in.Events = append(in.Events, connector.GroupEvent{ID: e.ID, Meta: e.Meta, Done: e.Done})
	}
	cctx, cancel := context.WithTimeout(ctx, groupCallTimeout)
	defer cancel()
	var res connector.GroupFallbackResult
	err := s.appCall(cctx, AppInbox, inboxGroupFallback, map[string]any{"source": c.ID, "fallback": in}, &res)
	if unsupported(err) {
		return nil, errNoFallback
	}
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, m := range t.Group.New {
		want[m] = true
	}
	take := func(m string) error {
		if !want[m] {
			return fmt.Errorf("message %q is not one of the batch's, or placed twice", m)
		}
		delete(want, m)
		return nil
	}
	plan := &groupPlan{}
	at := map[string]int{}
	for _, a := range res.Assign {
		if open, ok := listed[a.Event]; !ok || !open {
			return nil, fmt.Errorf("event %q is not an open one of those it was given", a.Event)
		}
		if err := take(a.Message); err != nil {
			return nil, err
		}
		i, ok := at[a.Event]
		if !ok {
			i = len(plan.events)
			at[a.Event] = i
			plan.events = append(plan.events, plannedEvent{existing: a.Event})
		}
		plan.events[i].messages = append(plan.events[i].messages, a.Message)
	}
	keys := map[string]bool{}
	for _, n := range res.New {
		if n.Key == "" || keys[n.Key] || len(n.Messages) == 0 {
			return nil, fmt.Errorf("new event %q: no key, a key twice, or no messages", n.Key)
		}
		keys[n.Key] = true
		for _, m := range n.Messages {
			if err := take(m); err != nil {
				return nil, err
			}
		}
		pe := plannedEvent{messages: n.Messages, title: n.Title}
		if n.RuleTier.Known() {
			pe.tier = string(n.RuleTier)
		}
		plan.events = append(plan.events, pe)
	}
	if len(want) > 0 {
		return nil, fmt.Errorf("%d message(s) placed nowhere", len(want))
	}
	return plan, nil
}

// writeGroup writes each event of the plan and keeps it: through the connector when it keeps the events' meta (w; an
// event that failed is tried again, alone), else composed here from the batch, without meta. An existing event done
// meanwhile (the user filed it) gets a new one that continues it instead.
func (s *Service) writeGroup(ctx context.Context, c assistantwire.InboxSource, t connector.InboxThing, plan *groupPlan) {
	b := &s.inbox
	b.mu.Lock()
	b.foldNoise(c.ID, t.Thread, plan)
	b.mu.Unlock()
	var saved []*InboxItem
	for _, e := range plan.events {
		a := connector.GroupAppend{Thread: t.Thread, Messages: e.messages, Continues: e.continues}
		if e.existing != "" {
			b.mu.Lock()
			if cur := b.items[e.existing]; cur != nil && cur.open() {
				a.Event, a.Meta = cur.ID, cur.Meta
			} else {
				a.Continues = e.existing
			}
			b.mu.Unlock()
		}
		var thing connector.InboxThing
		var err error
		if c.Meta {
			thing, err = s.appendEvent(ctx, c.ID, a)
		} else {
			thing = s.composeEvent(c.ID, t, a)
		}
		if err != nil {
			s.logf("inbox: %s: batch %s: writing %s: %v", c.ID, t.Group.Batch, orNew(a.Event), err)
			continue
		}
		e.continues = a.Continues // a done event's follow-up continues it (above)
		if it := s.putEvent(c, t.Thread, e, thing, plan.model); it != nil && !containsItem(saved, it) {
			saved = append(saved, it)
		}
	}
	// No rollback here (#4366): the batch is spent once its events are written (a connector with meta stops sending
	// what group.append took, and a repeated batch is related once), so an undone event would never come back and an
	// existing one would hand the connector stale meta. Memory keeps the events; the row catches up at their next save.
	for id := range s.saveInbox(ctx, saved) {
		s.logf("inbox: %s: event %s could not be saved; it stays in memory until its next save", c.ID, id)
	}
	if len(saved) > 0 {
		s.handInbox(ctx) // a new "Needs you" may be the AI twin's
	}
}

func orNew(event string) string {
	if event == "" {
		return "a new event"
	}
	return event
}

// appendEvent is one group.append (at the hub), tried groupAttempts times; an answer without an id is a failure. The
// tries carry one append id: one whose answer was lost on the way (the plugin wrote it) is answered by the hub from
// what the plugin said, not written again.
func (s *Service) appendEvent(ctx context.Context, source string, a connector.GroupAppend) (connector.InboxThing, error) {
	raw, _ := json.Marshal(a)
	appendID := namedClientID("append\n" + source + "\n" + string(raw))
	var err error
	wait := groupRetryWait
	for try := range groupAttempts {
		if try > 0 {
			select {
			case <-ctx.Done():
				return connector.InboxThing{}, ctx.Err()
			case <-time.After(wait):
			}
			wait *= 2
		}
		cctx, cancel := context.WithTimeout(ctx, groupCallTimeout)
		var res struct {
			Thing connector.InboxThing `json:"thing"`
		}
		err = s.appCall(cctx, AppInbox, inboxGroupAppend, map[string]any{"source": source, "append": a, "append_id": appendID}, &res)
		cancel()
		thing := res.Thing
		if err == nil && thing.ID == "" {
			err = errors.New("the answer has no id")
		}
		if err == nil {
			return thing, nil
		}
	}
	return connector.InboxThing{}, err
}

// putEvent keeps an event the connector wrote (or the hub composed), under the item's id: the model's title, summary
// and tier, or under the fallback its rule tier with "By rule"; triaged at this version either way. Its count is the
// hub's (the messages it put in it; extra.count is not read for an event) and its start is set when it is made. An answer at the version the item has
// already is a repeat: nothing is stored or announced again (nil).
func (s *Service) putEvent(c assistantwire.InboxSource, thread string, e plannedEvent, thing connector.InboxThing, byModel bool) *InboxItem {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := b.items[thing.ID]
	if it != nil && it.Last == thing.Last {
		return nil
	}
	var prev inboxSeen
	if it != nil {
		prev = seenOf(it)
	}
	if it == nil {
		it = &InboxItem{ID: thing.ID, Source: c.ID, Account: c.ID, AccountName: c.Name, StartedAt: thing.At}
		if x := thing.Extra; x != nil {
			if x.Source != "" {
				it.Source = x.Source
			}
			it.Account, it.AccountName = x.Account, x.AccountName
			if !x.StartedAt.IsZero() {
				it.StartedAt = x.StartedAt
			}
		}
	}
	count, started, title := it.Count, it.StartedAt, it.Title
	if thing.Extra != nil {
		fillMigrated(it, thing)
	} else {
		thing.Tier = connector.Tier(eventRuleTier(thing)) // the rule tier, not a decision: set below
		fillConnectorItem(it, thing)
	}
	// The hub assigns the messages, so it keeps the count (the connector's meta may hold only the newest ones): the
	// event had count, it takes these. Its start is set once, when it is made.
	it.Count, it.StartedAt = count+len(e.messages), started
	it.Meta, it.Group, it.Read = thing.Meta, &InboxGroup{Connector: c.ID, Thread: thread}, false
	b.noteReceived(it, prev, nil)
	tierBefore := it.Tier
	switch {
	case prev.last != "" && it.handled():
		// Messages that join an event being handled follow it: its tier, words and urgency stay; its processing
		// conversation is told (inbox_handle.go).
		it.Title, it.TriagedBy = title, orDefault(it.TriagedBy, "model")
	case byModel:
		it.Title, it.Summary, it.Tier, it.Reason, it.TriagedBy = clip(oneLine(e.title), 40), clip(oneLine(e.summary), 80), e.tier,
			clip(oneLine(e.reason), 30), "model"
		it.Urgent = e.urgent && e.tier == TierAction
	default:
		tier := e.tier
		if tier == "" {
			tier = eventRuleTier(thing)
		}
		if tier == "" {
			tier = b.ruleTier(it)
		}
		if tier == "" { // nothing says: the user looks (when unsure, pick action)
			tier = TierAction
		}
		if t := strings.TrimSpace(e.title); t != "" {
			it.Title = t
		}
		it.Summary, it.Tier, it.Reason, it.TriagedBy = "", tier, b.byRules(), "rules"
	}
	if !it.handled() { // what an event's conversation handles keeps its tier
		b.applyPolicyTier(it)
	}
	it.TriagedAt = it.Last
	if tierBefore != it.Tier || prev.last == "" {
		b.noteTriaged(it)
	}
	if e.continues != "" && e.continues != it.ID {
		b.offerParent(it, &InboxParent{Kind: ParentInbox, ID: e.continues, By: ParentByContinues})
	}
	b.offerParent(it, thingParent(thing))
	if m := mutedBy(b.mutes, it); m != nil && it.open() {
		b.fileMuted(it, time.Now(), m.Name)
	}
	b.items[it.ID] = it
	return it
}

// foldNoise keeps noise one event per atom (docs/assistant/inbox.md "Events and the handling chain"): the noise a plan opens goes into the
// conversation's open noise event, or into one new event for all of it; an existing noise event never takes what
// the model sorts higher (that opens its own event instead). Under b.mu.
func (b *inboxBook) foldNoise(connectorID, thread string, plan *groupPlan) {
	var open *InboxItem // the conversation's open noise event
	for _, it := range b.items {
		if it.Group != nil && it.Group.Connector == connectorID && it.Group.Thread == thread && it.open() && it.Tier == TierNoise &&
			(open == nil || newerItem(it, open)) {
			open = it
		}
	}
	out := plan.events[:0:0]
	noise := -1
	for _, e := range plan.events {
		cur := ""
		if e.existing != "" {
			if it := b.items[e.existing]; it != nil {
				cur = it.Tier
			}
		}
		if cur == TierNoise && e.tier != "" && e.tier != TierNoise {
			e.existing = "" // a noise event is not escalated: the rest is an event of its own
		}
		isNoise := e.tier == TierNoise || e.tier == "" && cur == TierNoise
		if isNoise {
			if noise >= 0 {
				out[noise].messages = append(out[noise].messages, e.messages...)
				continue
			}
			if e.existing == "" && open != nil {
				e.existing, e.continues = open.ID, ""
			}
			noise = len(out)
		}
		out = append(out, e)
	}
	plan.events = out
}

func orDefault(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// eventRuleTier is the tier the connector's rules give an event it wrote: extra's rule_tier, else its tier.
func eventRuleTier(t connector.InboxThing) string {
	if t.Extra != nil && connector.Tier(t.Extra.RuleTier).Known() {
		return t.Extra.RuleTier
	}
	if t.Tier.Known() {
		return string(t.Tier)
	}
	return ""
}

// jsonText is v as compact JSON for a prompt.
func jsonText(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

// argsText is a tool call's arguments as one line of JSON ({} when there are none).
func argsText(raw json.RawMessage) string {
	var v any
	if json.Unmarshal(raw, &v) != nil {
		return "{}"
	}
	return jsonText(v)
}
