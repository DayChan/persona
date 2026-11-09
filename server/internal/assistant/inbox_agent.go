package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// The inbox's AI twin (docs/assistant/inbox.md): model 1 triages every new item (inbox_triage.go), every "Needs you" item
// (and what an inbox policy hands it, inbox_policies.go) goes to an agent, model 2 links it to one of the user's
// tasks (linkTask), and the agent (model 3, a headless Claude Code in the assistant's
// directory, with its tools: a conversation the assistant starts itself, untitled) looks into it and drafts the reply
// (inbox_draft). The draft waits in the inbox; nothing is sent until the user taps "Send" (POST /api/v1/inbox/{id}/reply).

// InboxAgentConfig is manager.json's inbox.agent (the hub hands it over in InboxConfig).
type InboxAgentConfig = assistantwire.InboxAgentConfig

func agentConcurrency(c InboxAgentConfig) int { return max(c.Concurrency, 1) }

func agentTimeout(c InboxAgentConfig) time.Duration {
	if c.TimeoutMinutes <= 0 {
		return 15 * time.Minute
	}
	return time.Duration(c.TimeoutMinutes) * time.Minute
}

func agentMaxAge(c InboxAgentConfig) time.Duration {
	if c.MaxAgeHours <= 0 {
		return 24 * time.Hour
	}
	return time.Duration(c.MaxAgeHours) * time.Hour
}

const (
	AgentSkipped = "skipped" // model 2 kept it for the user
	AgentQueued  = "queued"
	AgentRunning = "running"
	AgentDrafted = "drafted"
	AgentFailed  = "failed"
)

// inboxAgentStartWait bounds one assistant.send: the node builds the conversation before it answers, which can take
// a while on a waking machine, so a timeout leaves the result unknown (the session may exist on the node).
var inboxAgentStartWait = 110 * time.Second

// inboxAgentStartMaxAttempts bounds starts whose result was unknown: the first try plus two retries.
const inboxAgentStartMaxAttempts = 3

// inboxAgentStartBackoff is the wait before the second and third starts, indexed by attempts already made minus one.
// The waits are covered by the hub's inbox rounds (it starts queued agents on every round, a couple of minutes).
var inboxAgentStartBackoff = []time.Duration{time.Minute, 5 * time.Minute}

// errNoInboxMachine is the definite "no computer can run the inbox agent" refusal before any node call.
var errNoInboxMachine = errors.New("no machine for the inbox agent")

// InboxAgent is the AI twin's work on one item.
type InboxAgent struct {
	State     string `json:"state"`
	Task      string `json:"task,omitempty"`   // model 2's instruction to the agent
	Reason    string `json:"reason,omitempty"` // model 2's why, short
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"` // the agent's conversation ("Chats" tab): how it got there
	Draft     string `json:"draft,omitempty"`
	Note      string `json:"note,omitempty"` // what the agent found, for the user to check the draft against
	// Changes are the status moves the agent proposes with its draft (document items); "Send" makes them.
	Changes []DocChange `json:"changes,omitempty"`
	Error   string      `json:"error,omitempty"`
	Of      string      `json:"of,omitempty"` // the item's Last the work answers
	// TodoID: the user's task the item is about (model 2's task_id, docs/assistant/unified-entry.md): the draft tells its
	// progress, and the task's conversation gets a card for it.
	TodoID    string     `json:"todo_id,omitempty"`
	StartedAt *time.Time `json:"started_at,omitempty"`
	DraftedAt *time.Time `json:"drafted_at,omitempty"`
	// Attempts is how many starts were made; RetryAfter keeps a queued agent whose last start's result was unknown
	// out of the next rounds until the backoff passed.
	Attempts   int        `json:"attempts,omitempty"`
	RetryAfter *time.Time `json:"retry_after,omitempty"`

	// The processing conversation's bookkeeping (inbox_handle.go), persisted, not listed: what waits to be told it
	// (sent with the next delivery, or carried by the first message of a new one), whether that asks it to work
	// (Wake: a turn starts), the item's Last when Persona sent a confirmed reply (a "replied" from the source then is
	// Persona's own message, not the user's), and the conversations found gone.
	Pending []inboxNote `json:"pending,omitempty"`
	Wake    bool        `json:"wake,omitempty"`
	SentOf  string      `json:"sent_of,omitempty"`
	Lost    []string    `json:"lost,omitempty"`
	// Back is the handling state a waking note interrupted (waiting, done…): a turn that only updates the recap
	// goes back to it.
	Back string `json:"back,omitempty"`
	// told counts the notes queued into Pending (in memory only): a message made of Pending carried those queued up
	// to its count, and the ones queued since are the newest. Pending is trimmed to the latest 20, so its length
	// cannot tell how many came since.
	told int
}

// queueNote adds a note to Pending, kept to the latest 20.
func (a *InboxAgent) queueNote(n inboxNote) {
	a.Pending = append(a.Pending, n)
	if len(a.Pending) > 20 {
		a.Pending = a.Pending[len(a.Pending)-20:]
	}
	a.told++
}

// keepToldSince drops the notes a message made at count told carried: only those queued since stay.
func (a *InboxAgent) keepToldSince(told int) {
	a.Pending = a.Pending[len(a.Pending)-min(max(a.told-told, 0), len(a.Pending)):]
}

// lost: conversation sid was found gone.
func (a *InboxAgent) lost(sid string) bool { return slices.Contains(a.Lost, sid) }

// startDue says a queued agent waits for no backoff (a fresh judge's agent has no RetryAfter).
func (a *InboxAgent) startDue(now time.Time) bool {
	return a.RetryAfter == nil || !a.RetryAfter.After(now)
}

// handInbox is one pass of the AI twin's work (docs/assistant/inbox.md"AI twin"): settle conversations that finished, hung or
// were lost, escalate every newly triaged Needs you item to the manager (one processing conversation per event), deliver
// what waits for the events' conversations (new messages, the user's answers), start conversations for the queued
// ones. One pass at a time, in the background. A pass asked for while one runs is not dropped: the goroutine running
// passes runs another after it (every caller is an event, and nothing else would come back for what it queued).
func (s *Service) handInbox(ctx context.Context) {
	b := &s.inbox
	if !s.inboxConfig(ctx).Agent.Enabled {
		b.mu.Lock()
		pending := false
		for _, it := range b.items {
			active, queued := inboxAgentReleaseState(it)
			if active || queued {
				pending = true
				break
			}
		}
		b.mu.Unlock()
		if !pending {
			return
		}
	}
	b.handAgain.Store(true)
	if !b.handing.TryLock() {
		return // the running passes' goroutine finds the mark
	}
	ctx = context.WithoutCancel(ctx) // like triageInbox: the pass outlives the caller
	go func() {
		for {
			for b.handAgain.Swap(false) {
				s.handPass(ctx)
			}
			b.handing.Unlock()
			// A call that came after the last check found the lock still held: its mark is set, and it is run here
			// unless a call since took the lock (and runs it).
			if !b.handAgain.Load() || !b.handing.TryLock() {
				return
			}
		}
	}()
}

// handPass is one pass of handInbox, with b.handing held.
func (s *Service) handPass(ctx context.Context) {
	cfg := s.inboxConfig(ctx).Agent
	b := &s.inbox
	if !cfg.Enabled {
		s.releaseInboxAgents(ctx)
		return
	}
	if f := b.passHook; f != nil {
		f()
	}
	now := time.Now()
	s.sweepInboxAgents(ctx, cfg, now)
	escalated := b.escalate(now.Add(-agentMaxAge(cfg)), now)
	if tasks := s.activeTasks(ctx); len(tasks) > 0 { // which of the user's tasks a new one is about
		for _, w := range escalated {
			b.mu.Lock()
			fresh := w.it.Agent != nil && w.it.Agent.SessionID == "" && w.it.Agent.TodoID == ""
			b.mu.Unlock()
			if fresh {
				s.linkTaskFor(ctx, w.it, tasks, w)
			}
		}
	}
	// A failed row gives the event back its old state: the next pass escalates it again.
	s.saveWrites(ctx, escalated, "its escalation")
	s.deliverPending(ctx)
	s.startInboxAgents(ctx, cfg)
}

// releaseInboxAgents returns unfinished twin work to the user even while promotion and starts are disabled.
func (s *Service) releaseInboxAgents(ctx context.Context) {
	b := &s.inbox
	b.mu.Lock()
	var changed []*inboxWrite
	now := time.Now()
	for _, it := range b.items {
		if w := b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now); w != nil {
			changed = append(changed, w)
		}
	}
	b.mu.Unlock()
	s.saveWrites(ctx, changed, "the disabled AI twin's work")
}

// releaseInboxAgent drops queued work and releases an unfinished item. Completed suggestions and confirmed
// actions waiting for a reply stay available. HandlingDone restores the item's tier; HandlingFailed returns it to
// Needs you. The caller holds b.mu and persists the returned write.
func (b *inboxBook) releaseInboxAgent(it *InboxItem, state, reason string, now time.Time) *inboxWrite {
	active, pending := inboxAgentReleaseState(it)
	if !active && !pending {
		return nil
	}
	w := beginWrite(it)
	if active {
		if state == HandlingFailed {
			b.failHandling(it, reason, now)
		} else {
			b.setHandling(it, state, "", now)
			b.chainAdd(it, InboxChainEntry{Kind: ChainConcluded, At: now.UTC(), Text: reason})
		}
	}
	if a := it.Agent; a != nil {
		a.Pending, a.Wake, a.Back, a.RetryAfter = nil, false, "", nil
	}
	b.settle(w)
	return w
}

// inboxAgentReleaseState identifies unfinished handling and queued work while the caller holds the inbox lock.
func inboxAgentReleaseState(it *InboxItem) (active, pending bool) {
	a, h := it.Agent, it.Handling
	active = it.open() && (h != nil && (h.State == HandlingLooking || h.State == HandlingAgent) ||
		h == nil && a != nil && (a.State == AgentQueued || a.State == AgentRunning))
	pending = a != nil && (len(a.Pending) > 0 || a.Wake || a.Back != "" || a.RetryAfter != nil)
	return active, pending
}

// startInboxAgents starts a processing conversation for the queued events that have none (newest first, while fewer
// than the configured number are looking): one the event's id finds (a start the manager restarted during) is taken
// back instead; one that comes after a lost conversation carries the recap and the chain.
func (s *Service) startInboxAgents(ctx context.Context, cfg InboxAgentConfig) {
	b := &s.inbox
	linked := map[string]*prompts.InboxTask{} // read before the inbox lock (todos and sessions)
	for _, t := range s.activeTasks(ctx) {
		linked[t.ID] = &t
	}
	owner := s.owner(ctx)
	loc := s.location(ctx) // the chain's times, read in the user's zone (the hub runs in UTC)
	now := time.Now().UTC()
	byEvent := s.conversationsByEvent(ctx)
	b.mu.Lock()
	running := 0
	var queued []*InboxItem
	var released []*inboxWrite
	for _, it := range b.items {
		if it.Agent == nil {
			continue
		}
		switch {
		case it.Agent.SessionID != "" && (it.Handling == nil && it.Agent.State == AgentRunning || it.Handling != nil && it.Handling.State == HandlingLooking):
			running++
		case it.Agent.State == AgentQueued && it.Agent.SessionID == "" && it.open():
			// Recheck before backoff, capacity or adoption: a saved rule also covers work already queued.
			if b.policyTwin(it) == TwinNever {
				if w := b.releaseInboxAgent(it, HandlingDone, i18n.T(b.userLang(), "assistant-inbox_agent.never"), now); w != nil {
					released = append(released, w)
				}
				continue
			}
			if it.Agent.startDue(now) {
				queued = append(queued, it)
			}
		}
	}
	b.sortQueued(queued)
	type job struct {
		it      *InboxItem
		chat    string
		prompt  func(*prompts.KBContext) string
		carried int // the agent's told count when the first message was made: it carries the notes queued up to it
	}
	var jobs []job
	var adopted []*inboxWrite
	larkDefault := max(b.larkAccount, 1)
	for _, it := range queued {
		if ref, ok := byEvent[it.ID]; ok && !it.Agent.lost(ref.SessionID) { // the event's conversation is there: go on in it
			w := beginWrite(it)
			it.Agent.MachineID, it.Agent.SessionID, it.Agent.Wake = ref.MachineID, ref.SessionID, true
			b.setHandling(it, HandlingLooking, "", now)
			b.settle(w)
			adopted = append(adopted, w)
			continue
		}
		if len(jobs) >= max(agentConcurrency(cfg)-running, 0) {
			continue
		}
		it.Agent.State, it.Agent.StartedAt, it.Agent.Attempts, it.Agent.RetryAfter = AgentRunning, &now, it.Agent.Attempts+1, nil
		a := b.inboxAgentFor(it, owner, linked[it.Agent.TodoID], larkDefault, loc)
		a.KnowledgeDir = s.kb.dir // the heading of what the knowledge base knows names where it is
		jobs = append(jobs, job{it, kbChatOf(it), func(kbc *prompts.KBContext) string { a.KB = kbc; return prompts.RenderInboxAgent(a) },
			it.Agent.told})
	}
	b.mu.Unlock()
	// A failed release stays queued for the next pass, but never starts under the rule.
	s.saveWrites(ctx, released, "the AI twin policy's queued work")
	// An adoption whose row failed is undone (no conversation again): its notes wait, and the next pass adopts it.
	s.saveWrites(ctx, adopted, "the adoption of its conversation")
	for _, w := range adopted {
		s.deliverNotes(ctx, w.it) // nothing goes for an undone one (no conversation)
	}
	for _, j := range jobs {
		var kbc *prompts.KBContext
		if j.chat != "" {
			kbc = s.kbChatContext(ctx, j.chat)
		}
		cctx, cancel := context.WithTimeout(ctx, inboxAgentStartWait)
		machineID, sessionID, err := s.startInboxAgent(cctx, cfg, j.it, j.prompt(kbc))
		cancel()
		b.mu.Lock()
		a := j.it.Agent
		// A note that asks for work while the start was on its way queued the event again (tell, no conversation yet):
		// the conversation that started is that work, and the note goes into it (#5366).
		woken := a != nil && a.State == AgentQueued && a.Wake && a.SessionID == "" && err == nil
		tellNow := false
		if a != nil && (a.State == AgentRunning || woken) && a.SessionID == "" {
			if err != nil {
				// The item may have been replied to or filed while the start was in flight: it never queues again.
				s.requeueAgentStart(ctx, a, err, time.Now().UTC(), j.it.open())
				if a.State == AgentFailed {
					b.failHandling(j.it, a.Error, time.Now())
				}
				s.logf("inbox: agent for %s: %v", j.it.ID, err)
			} else {
				a.State, a.MachineID, a.SessionID = AgentRunning, machineID, sessionID
				// The first message carried the notes pending when it was made; those told since still go.
				a.keepToldSince(j.carried)
				tellNow = len(a.Pending) > 0
				a.Lost = nil
				b.noteHandling(j.it, machineID, sessionID)
				b.setHandling(j.it, HandlingLooking, "", time.Now())
			}
		}
		b.mu.Unlock()
		// No rollback here: the start had its effect on a machine (a live conversation, or a refused start counted in
		// the attempts). Undone, the next pass would start a second conversation with a fresh attempt budget, and the
		// live one's suggestion would find no event. The row catches up at the item's next save.
		if failed := s.saveInbox(ctx, []*InboxItem{j.it}); failed[j.it.ID] {
			s.logf("inbox: agent for %s: its start could not be saved; the live state stays until the next save", j.it.ID)
		}
		if tellNow {
			s.deliverNotes(ctx, j.it)
		}
	}
	if len(jobs) > 0 {
		s.publish(ctx) // the hub knows the agents' conversations (no finished push for them)
	}
}

// kbChatOf is the knowledge base's chat of an item: its Feishu chat, or the mail's correspondent; "" for none.
func kbChatOf(it *InboxItem) string {
	switch {
	case it.Lark != nil:
		return kb.ChatID("lark", it.Lark.FeedID)
	case it.Mail != nil:
		return kb.ChatID("mail", it.Mail.AccountID+"-"+strings.ToLower(it.Mail.From.Email))
	}
	return ""
}

// startInboxAgent starts a headless conversation like the assistant's (same directory, so the same tools) on the
// inbox agent's model (model settings › inbox.agent), untitled: the draft in the inbox is its result.
func (s *Service) startInboxAgent(ctx context.Context, cfg InboxAgentConfig, it *InboxItem, prompt string) (machineID, sessionID string, err error) {
	machine := s.settings(ctx).MachineID
	if _, ok := s.device(ctx, cfg.Machine); ok && cfg.Machine != "" {
		machine = cfg.Machine
	}
	if _, ok := s.device(ctx, machine); !ok || machine == "" {
		return "", "", errNoInboxMachine
	}
	if s.isWorkMachine(machine) {
		return "", "", errWorkAssistant
	}
	sess, err := s.Start(ctx, StartParams{Text: prompt, Purpose: purposeAgent, MachineID: machine, Untitled: true, Origin: OriginInbox,
		OriginRef: it.ID})
	if err != nil {
		return "", "", err
	}
	// "MMDD | Inbox | the item's title" from the start, not its prompt's first line (#3264).
	s.titleFixed(sess.MachineID, sess.SessionID, time.Now(), titleInbox, it.Title)
	return sess.MachineID, sess.SessionID, nil
}

// definiteStartError says a start refusal means nothing began on the node and a retry cannot help: no computer
// (before the call or the hub's no_device answer), a computer still pending confirmation, Claude Code or the
// launcher missing, or a node that must be updated first. Anything else (a timeout, a dropped RPC, an offline or
// waking machine, an unknown error) leaves the result unknown and is retried.
func definiteStartError(err error) bool {
	if errors.Is(err, errNoInboxMachine) || errors.Is(err, ErrNoDevice) || errors.Is(err, errWorkAssistant) {
		return true
	}
	var re *rpc.Error
	if errors.As(err, &re) {
		switch re.Code {
		case proto.CodeNoMachine, proto.CodeMachinePending, proto.CodeClaudeMissing,
			proto.CodeLauncherMissing, proto.CodeNodeOutdated:
			return true
		}
	}
	return false
}

// requeueAgentStart settles an agent whose start answered without a conversation. A definite refusal fails it at
// once; an unknown result queues it again with backoff until the attempts are spent, then fails it. canRetry is
// false for an item settled (replied or filed) while the start was in flight: it must not come back queued. Either
// way the item keeps a localized plain sentence, never the error's own words. The caller holds the inbox lock.
func (s *Service) requeueAgentStart(ctx context.Context, a *InboxAgent, err error, now time.Time, canRetry bool) {
	definite := definiteStartError(err)
	a.MachineID, a.SessionID, a.StartedAt, a.Error = "", "", nil, ""
	if !definite && canRetry && a.Attempts < inboxAgentStartMaxAttempts {
		i := min(a.Attempts-1, len(inboxAgentStartBackoff)-1)
		at := now.Add(inboxAgentStartBackoff[i])
		a.State, a.RetryAfter = AgentQueued, &at
		return
	}
	a.RetryAfter = nil
	a.State = AgentFailed
	if !definite && canRetry { // retried until the attempts were spent
		a.Error = i18n.T(s.lang(ctx), "assistant-inbox_agent.start_gave_up")
	} else {
		a.Error = i18n.T(s.lang(ctx), "assistant-inbox_agent.start_failed")
	}
}

// sweepInboxAgents settles the events' conversations: one looking that ended its turn without a suggestion (after its
// first minute) or ran past the timeout fails, so does one whose sub-session ran past four timeouts; a start the
// manager did not live to record goes back to the queue (the event's id finds its conversation).
func (s *Service) sweepInboxAgents(ctx context.Context, cfg InboxAgentConfig, now time.Time) {
	b := &s.inbox
	b.mu.Lock()
	var running []*InboxItem
	var changed []*inboxWrite
	for _, it := range b.items {
		a, h := it.Agent, it.Handling
		switch {
		case a == nil:
		case a.SessionID != "" && (a.State == AgentRunning || h != nil && (h.State == HandlingLooking || h.State == HandlingAgent)):
			running = append(running, it)
		case a.State != AgentRunning || a.SessionID != "":
		case a.StartedAt == nil || now.Sub(*a.StartedAt) > inboxAgentStartWait+time.Minute:
			w := beginWrite(it)
			a.State, a.StartedAt = AgentQueued, nil
			b.settle(w)
			changed = append(changed, w)
		}
	}
	b.mu.Unlock()
	for _, it := range running {
		b.mu.Lock()
		a := *it.Agent
		state := HandlingLooking
		if it.Handling != nil {
			state = it.Handling.State
		}
		b.mu.Unlock()
		reason := ""
		switch {
		case s.isWorkMachine(a.MachineID):
			reason = i18n.T(s.lang(ctx), "assistant-start.work_machine")
		case state == HandlingAgent:
			if a.StartedAt != nil && now.Sub(*a.StartedAt) > 4*agentTimeout(cfg) {
				reason = i18n.T(s.lang(ctx), "assistant-inbox_agent.timeout")
			}
		case state != HandlingLooking:
		case a.StartedAt != nil && now.Sub(*a.StartedAt) > agentTimeout(cfg):
			reason = i18n.T(s.lang(ctx), "assistant-inbox_agent.timeout")
		case a.StartedAt != nil && now.Sub(*a.StartedAt) > time.Minute:
			if sess, ok := s.session(ctx, a.MachineID, a.SessionID); ok && sess.Status != proto.StatusWorking {
				reason = i18n.T(s.lang(ctx), "assistant-inbox_agent.no_draft")
			}
		}
		if reason == "" {
			continue
		}
		b.mu.Lock()
		if it.Agent != nil && it.Agent.SessionID == a.SessionID && (it.Handling == nil || it.Handling.State == state) {
			w := beginWrite(it)
			if it.Suggestion != nil { // new messages left its suggestion standing: it still holds
				b.setHandling(it, HandlingDone, "", now)
			} else {
				b.failHandling(it, reason, now)
			}
			b.settle(w)
			changed = append(changed, w)
		}
		b.mu.Unlock()
	}
	// Undone, a settled conversation is still running in memory: the next sweep settles it again.
	s.saveWrites(ctx, changed, "its settled conversation")
}

// inboxAgentPrompt is the AI twin's first message for an item (prompts/inbox/agent.md).
func (b *inboxBook) inboxAgentPrompt(it *InboxItem, owner string, kbc *prompts.KBContext) string {
	return b.inboxAgentPromptFor(it, owner, kbc, nil, 1, time.UTC)
}

// inboxAgentPromptFor is inboxAgentPrompt for an item about one of the user's tasks (linked: its progress).
// larkDefault is lark.account: the Feishu account lark_messages reads unless the prompt names another.
// loc: the user's time zone, which the chain's times are read in.
func (b *inboxBook) inboxAgentPromptFor(it *InboxItem, owner string, kbc *prompts.KBContext, linked *prompts.InboxTask, larkDefault int, loc *time.Location) string {
	a := b.inboxAgentFor(it, owner, linked, larkDefault, loc)
	a.KB = kbc
	return prompts.RenderInboxAgent(a)
}

// inboxAgentFor is what the AI twin's first message is made of, but the knowledge base's part.
func (b *inboxBook) inboxAgentFor(it *InboxItem, owner string, linked *prompts.InboxTask, larkDefault int, loc *time.Location) prompts.InboxAgent {
	a := prompts.InboxAgent{Owner: owner, EventID: it.ID, Title: it.Title, Item: b.promptItem(it), Task: it.Agent.Task, Reason: it.Agent.Reason,
		Linked: linked}
	if len(it.Agent.Lost) > 0 { // the event's conversation is gone: where it got to goes with the new one
		a.Resumed, a.Recap = true, it.Recap
		for _, e := range it.Chain {
			a.Chain = append(a.Chain, chainLine(e, loc))
		}
	}
	for _, n := range it.Agent.Pending {
		if n.Name != prompts.NoteNewMessages { // the item above is its content now
			a.Notes = append(a.Notes, renderNote(n, owner))
		}
	}
	switch {
	case it.Lark != nil:
		a.Lark = &prompts.InboxAgentLark{FeedID: it.Lark.FeedID}
		if account := larkItemAccount(it); account != larkDefault {
			a.Lark.Account = account
		}
	case it.Doc != nil:
		a.Doc = &prompts.InboxAgentDoc{URL: it.Doc.URL, Rows: len(it.Doc.Rows) > 0}
	case it.Moment != nil:
		a.Moment = &prompts.InboxAgentMoment{PostID: it.Moment.PostID}
	case it.Connector != nil:
		a.Connector = &prompts.InboxAgentConnector{Source: it.Source, URL: it.Connector.URL, ReplyTo: it.Connector.ReplyTo}
	case it.Mail != nil && len(it.Mail.UIDs) > 0:
		a.Mail = &prompts.InboxAgentMail{Account: it.Mail.AccountID, Mailbox: it.Mail.Mailbox, UID: it.Mail.UIDs[len(it.Mail.UIDs)-1]}
	}
	return a
}

// itemOfAgent is the item whose agent runs in conversation sessionID. Under b.mu.
func (b *inboxBook) itemOfAgent(sessionID string) *InboxItem {
	if sessionID == "" {
		return nil
	}
	for _, it := range b.items {
		if it.Agent != nil && it.Agent.SessionID == sessionID {
			return it
		}
	}
	return nil
}

// inboxDraftWait bounds how long a draft waits for its agent's start to record the conversation (agentStarting).
var inboxDraftWait = 15 * time.Second

// agentStarting: an agent is being started and does not know its conversation yet. Callers hold b.mu.
func (b *inboxBook) agentStarting() bool {
	for _, it := range b.items {
		if it.Agent != nil && it.Agent.State == AgentRunning && it.Agent.SessionID == "" {
			return true
		}
	}
	return false
}

// messageCardOf is the card for an item whose draft is about a task; nil when it is about none. Under b.mu.
func messageCardOf(it *InboxItem) *MessageCard {
	if it.Agent == nil || it.Agent.TodoID == "" || it.Agent.Draft == "" {
		return nil
	}
	return &MessageCard{TodoID: it.Agent.TodoID, Source: it.Source, Account: it.AccountName, Chat: it.Title, Sender: it.Sender,
		Text: clip(strings.TrimSpace(it.Preview), 400), Draft: it.Agent.Draft}
}

// inboxRepliedAt is when those of the inbox items ids the user replied to were replied to (the message cards say
// sent by it).
func (s *Service) inboxRepliedAt(ctx context.Context, ids []string) map[string]time.Time {
	b := &s.inbox
	out := map[string]time.Time{}
	if s.inboxReady(ctx) != nil {
		return out
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if it := b.items[id]; it != nil && it.DoneBy == "replied" && it.DoneAt != nil {
			out[id] = *it.DoneAt
		}
	}
	return out
}

// inboxFiledAt is when those of the inbox items ids that are filed (not open) were filed.
func (s *Service) inboxFiledAt(ctx context.Context, ids []string) map[string]time.Time {
	b := &s.inbox
	out := map[string]time.Time{}
	if s.inboxReady(ctx) != nil {
		return out
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, id := range ids {
		if it := b.items[id]; it != nil && !it.open() {
			out[id] = *it.DoneAt
		}
	}
	return out
}

// ActiveTasks are the tasks going on, as the inbox's judge and AI twin link messages to them.
func (s *Service) ActiveTasks(ctx context.Context) []prompts.InboxTask { return s.activeTasks(ctx) }
