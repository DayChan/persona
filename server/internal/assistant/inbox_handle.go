package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// The processing conversation (docs/assistant/inbox.md"AI twin", phase 1): with the AI twin on, every Needs you event goes to the
// manager, one conversation per event, bound to it by the event's id. What comes later — new messages, the user's
// confirmation, rejection or words, the event being closed — is sent into that same conversation (sendToSession)
// instead of starting over; a conversation found gone is replaced by a new one that carries the recap and the chain.
// The conversation hands in one suggestion at a time (inbox_suggest); what others would see runs only once the user
// confirms (POST /api/v1/inbox/{id}/suggestion), and then by the program, exactly as shown.

// inboxNote is something waiting to be told an event's conversation (prompts/inbox/handling/<Name>.md).
type inboxNote struct {
	Name    string `json:"name"`
	Text    string `json:"text,omitempty"`
	Edited  bool   `json:"edited,omitempty"`
	Context string `json:"context,omitempty"`
	Machine string `json:"machine,omitempty"`
	Session string `json:"session,omitempty"`
}

func renderNote(n inboxNote, owner string) string {
	return prompts.RenderInboxHandlingNote(n.Name, prompts.InboxHandlingNote{Owner: owner, Text: n.Text, Edited: n.Edited,
		Context: n.Context, Machine: n.Machine, Session: n.Session})
}

// chainLine is a chain entry as the conversation reads it, its time in loc (the user's zone).
func chainLine(e InboxChainEntry, loc *time.Location) string {
	line := e.At.In(loc).Format("01-02 15:04") + " " + e.Text
	if e.Detail != "" && !strings.Contains(e.Text, e.Detail) {
		line += "（" + clip(oneLine(e.Detail), 200) + "）" // i18n-ignore: a line of the prompt the model reads (prompts/inbox/agent.md)
	}
	return line
}

// tell queues a note for the event's conversation; wake: it asks the conversation to work (a turn starts). With no
// conversation, a waking note queues one (startInboxAgents carries the notes in its first message). Under b.mu.
func (b *inboxBook) tell(it *InboxItem, n inboxNote, wake bool, now time.Time) {
	a := it.Agent
	if a == nil {
		return
	}
	a.queueNote(n)
	if !wake {
		return
	}
	a.Wake = true
	if h := it.Handling; h != nil && h.State != HandlingLooking {
		a.Back = h.State
	}
	t := now.UTC()
	a.StartedAt = &t
	if a.SessionID == "" {
		a.State, a.Attempts, a.RetryAfter, a.Error = AgentQueued, 0, nil, ""
	}
}

// setHandling moves the event's processing state, and mirrors it on Agent for the apps from before (an Agent's
// running, drafted with the reply, failed). Under b.mu.
func (b *inboxBook) setHandling(it *InboxItem, state, errText string, now time.Time) {
	h := it.Handling
	if h == nil {
		h = &InboxHandling{Owner: HandlingOwner}
		it.Handling = h
	}
	if h.State != state || h.Since == nil {
		t := now.UTC()
		h.Since = &t
	}
	h.State, h.Error = state, errText
	a := it.Agent
	if a == nil {
		return
	}
	h.MachineID, h.SessionID = a.MachineID, a.SessionID
	if g := it.Suggestion; g != nil && g.Kind == SuggestReply && !g.Handback {
		at := g.At
		a.State, a.Draft, a.Note, a.Changes, a.Error, a.DraftedAt = AgentDrafted, g.Text, g.Note, g.Changes, "", &at
		return
	}
	a.Draft, a.Note, a.Changes = "", "", nil
	switch state {
	case HandlingLooking, HandlingAgent:
		if a.State != AgentQueued {
			a.State = AgentRunning
		}
		a.Error = ""
	case HandlingFailed:
		a.State, a.Error = AgentFailed, errText
	default:
		a.State, a.Error = AgentSkipped, ""
	}
}

// failHandling: the conversation failed or timed out: back to Needs you without a suggestion, the chain says why.
// Under b.mu.
func (b *inboxBook) failHandling(it *InboxItem, reason string, now time.Time) {
	it.Suggestion = nil
	if it.Agent != nil {
		it.Agent.Wake = false
	}
	b.setHandling(it, HandlingFailed, reason, now)
	b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.failed"), Detail: reason})
}

// closeHandling: the event is over (the user filed it, replied elsewhere…): its suggestion goes, and a conversation
// still on it (or with a suggestion out) is told to stop; why is how (prompts/inbox/handling/closed.md). It says
// whether there is something to tell. Under b.mu.
func (b *inboxBook) closeHandling(it *InboxItem, why string, now time.Time) bool {
	a := it.Agent
	if a == nil || it.Handling == nil {
		return false
	}
	active := it.handled() && !(it.Suggestion != nil && it.Suggestion.Kind == SuggestConclusion && it.Handling.State == HandlingDone)
	it.Suggestion = nil
	a.Wake, a.Pending = false, nil
	b.setHandling(it, HandlingDone, "", now)
	if !active || a.SessionID == "" {
		return false
	}
	a.queueNote(inboxNote{Name: prompts.NoteClosed, Text: why})
	return true
}

// ownSend: the source says the newest message of the item is the user's, and it is the reply Persona sent for a
// confirmed suggestion (nothing came in since): not the user's own reply. Under b.mu.
func ownSend(it *InboxItem, last string) bool {
	a, h := it.Agent, it.Handling
	return a != nil && h != nil && h.State == HandlingWaiting && a.SentOf != "" && a.SentOf == it.Last && (last == "" || last == it.Last)
}

// escalate hands the manager its work, under b.mu: a newly triaged Needs you event (recent) gets a processing
// conversation (queued); new content of an event that has one — being handled, or triaged Needs you again — is sent into
// that conversation. It returns the items it moved, settled (inbox_rollback.go).
func (b *inboxBook) escalate(since, now time.Time) []*inboxWrite {
	b.mu.Lock()
	defer b.mu.Unlock()
	var changed []*inboxWrite
	for _, it := range b.items {
		a := it.Agent
		if !it.open() || it.Context == "" || a != nil && a.Of == it.Last {
			continue
		}
		handled := it.handled()
		twin := b.policyTwin(it) // the user's policy (inbox_policies.go): first takes what is not Needs you too
		if !handled && (twin == TwinNever || it.Tier != TierAction && twin != TwinFirst || it.TriagedAt != it.Last) {
			continue
		}
		w := beginWrite(it)
		if a == nil {
			if !it.UpdatedAt.After(since) {
				continue
			}
			a = &InboxAgent{State: AgentQueued}
			it.Agent = a
		}
		a.Of, a.Task, a.Reason = it.Last, it.Summary, it.Reason
		// (a first message has the item's content: startInboxAgents leaves this one out of it)
		b.tell(it, inboxNote{Name: prompts.NoteNewMessages, Context: it.Context}, true, now)
		b.setHandling(it, HandlingLooking, "", now)
		b.settle(w)
		changed = append(changed, w)
	}
	return changed
}

// conversationsByEvent are the processing conversations by the event they were started for (their origin's ref),
// the newest of each.
func (s *Service) conversationsByEvent(ctx context.Context) map[string]SessionRef {
	b := &s.origins
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.originsLocked(ctx) {
		return nil
	}
	out, at := map[string]SessionRef{}, map[string]time.Time{}
	for sid, o := range b.m {
		if o.Origin == OriginInbox && o.Ref != "" && !s.isWorkMachine(o.MachineID) && (at[o.Ref].IsZero() || o.At.After(at[o.Ref])) {
			out[o.Ref], at[o.Ref] = SessionRef{MachineID: o.MachineID, SessionID: sid}, o.At
		}
	}
	return out
}

// deliverPending delivers what waits for the events' conversations.
func (s *Service) deliverPending(ctx context.Context) {
	b := &s.inbox
	b.mu.Lock()
	var todo []*InboxItem
	for _, it := range b.items {
		if a := it.Agent; a != nil && len(a.Pending) > 0 && a.SessionID != "" {
			todo = append(todo, it)
		}
	}
	b.mu.Unlock()
	for _, it := range todo {
		s.deliverNotes(ctx, it)
	}
}

// deliverNotes sends what waits for the event's conversation into it. A conversation found gone is dropped: a note
// that asks for work then queues a new one (carrying the recap and the chain); a send that failed otherwise is tried
// again on the next pass.
func (s *Service) deliverNotes(ctx context.Context, it *InboxItem) {
	b := &s.inbox
	v, _ := b.telling.LoadOrStore(it.ID, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	enabled := s.inboxConfig(ctx).Agent.Enabled
	owner := s.owner(ctx)
	b.mu.Lock()
	if cur := b.items[it.ID]; cur != it {
		// Forgotten (drop) while this was on its way: nothing goes, and the lock this may have put back goes again. One
		// of an item kept under the id since is another's, and stays.
		if cur == nil {
			b.telling.CompareAndDelete(it.ID, mu)
		}
		b.mu.Unlock()
		return
	}
	if !enabled {
		w := b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), time.Now())
		b.mu.Unlock()
		if w != nil {
			s.saveWrites(ctx, []*inboxWrite{w}, "the disabled AI twin's work")
		}
		return
	}
	a := it.Agent
	if a == nil || a.SessionID == "" || len(a.Pending) == 0 {
		b.mu.Unlock()
		return
	}
	machine, sid, wake, told := a.MachineID, a.SessionID, a.Wake, a.told
	notes := append([]inboxNote(nil), a.Pending...)
	b.mu.Unlock()
	if s.isWorkMachine(machine) {
		b.mu.Lock()
		if it.Agent == a && a.SessionID == sid {
			b.failHandling(it, i18n.T(s.lang(ctx), "assistant-start.work_machine"), time.Now())
		}
		b.mu.Unlock()
		s.saveInbox(ctx, []*InboxItem{it})
		return
	}
	texts := make([]string, len(notes))
	for i, n := range notes {
		texts[i] = renderNote(n, owner)
	}
	err := s.sendToSession(ctx, machine, sid, strings.Join(texts, "\n\n---\n\n"))
	gone := false
	if err != nil {
		// Gone only when its machine answers without it (an offline machine is tried again later).
		if _, known := s.device(ctx, machine); known {
			_, found := s.session(ctx, machine, sid)
			gone = !found
		}
		s.logf("inbox: tell the conversation of %s: %v (gone: %v)", it.ID, err, gone)
	}
	now := time.Now()
	b.mu.Lock()
	if it.Agent == a && a.SessionID == sid {
		switch {
		case err == nil:
			a.keepToldSince(told) // the notes told while it was sent go next time
			if wake {
				t := now.UTC()
				a.Wake, a.StartedAt = false, &t
			}
		case gone:
			a.Lost = append(a.Lost, sid)
			a.MachineID, a.SessionID = "", ""
			if a.Wake {
				a.State, a.Attempts, a.RetryAfter = AgentQueued, 0, nil
			} else {
				a.Pending = nil
			}
			if it.Handling != nil {
				it.Handling.MachineID, it.Handling.SessionID = "", ""
			}
		}
	}
	b.mu.Unlock()
	if err == nil || gone {
		s.saveInbox(ctx, []*InboxItem{it})
	}
	if gone && wake {
		s.handInbox(ctx)
	}
}

// tellSoon delivers an item's notes in the background (a request answers first).
func (s *Service) tellSoon(ctx context.Context, it *InboxItem) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		s.deliverNotes(ctx, it)
		s.handInbox(ctx) // one without a conversation is started
	}()
}

// ---- the suggestion tool (inbox_suggest; inbox_draft is its older name for a reply) ----

type suggestIn struct {
	Kind     string      `json:"kind"`
	Text     string      `json:"text"`
	Note     string      `json:"note"`
	Handback bool        `json:"handback"`
	Outcome  string      `json:"outcome"`
	Recap    string      `json:"recap"`
	Machine  string      `json:"machine"`
	Cwd      string      `json:"cwd"`
	Related  []string    `json:"related"`
	Changes  []DocChange `json:"changes"`
	// Item names the event: needed from a conversation it was handed to (Hand to AI, inbox_handoff.go).
	Item string `json:"item"`
}

var errNotProcessing = i18n.ErrEnglish("inbox-errors.not_processing")

// errSuggestion is a suggestion the tool refuses, said so the model can hand in a better one.
type errSuggestion struct{ msg string }

func (e errSuggestion) Error() string { return e.msg }

// check holds a suggestion to the rules before anything is kept.
func (in *suggestIn) check() error {
	in.Kind, in.Text, in.Outcome = strings.TrimSpace(in.Kind), strings.TrimSpace(in.Text), strings.TrimSpace(in.Outcome)
	switch in.Kind {
	case "":
		if strings.TrimSpace(in.Recap) == "" {
			return errSuggestion{"give kind (reply, agent or conclusion) with text, or at least recap"}
		}
		return nil
	case SuggestReply, SuggestAgent, SuggestConclusion:
	default:
		return errSuggestion{fmt.Sprintf("kind %q: one of reply, agent, conclusion", in.Kind)}
	}
	if in.Text == "" {
		return errSuggestion{"text is required"}
	}
	if in.Kind == SuggestReply && !in.Handback {
		// The user sends it with one tap: a blank left to fill in (<expected completion time>) would go out as it is.
		if p := draftPlaceholder(in.Text); p != "" {
			return errSuggestion{fmt.Sprintf("the reply leaves %s to fill in, and it goes out as it is: leave out what you could not find (say so in note) and submit again", p)}
		}
	}
	if in.Kind != SuggestReply && len(in.Changes) > 0 {
		return errSuggestion{"changes go with a reply only"}
	}
	if in.Kind == SuggestConclusion {
		switch in.Outcome {
		case "":
			in.Outcome = TierFYI
		case TierFYI, "done":
		default:
			return errSuggestion{fmt.Sprintf("outcome %q: fyi or done", in.Outcome)}
		}
	}
	return nil
}

// suggest keeps the suggestion of the conversation sessionID on its event: the item's id, or a change the item does
// not offer (nothing kept then); errNotProcessing when no event's conversation runs there.
func (s *Service) suggest(ctx context.Context, sessionID string, in suggestIn) (string, *DocChange, error) {
	id, refused, _, err := s.suggestFrom(ctx, sessionID, in)
	return id, refused, err
}

// suggestFrom is suggest, also naming the conversation the event was handed to when the suggestion came from one
// (in.Item; nil for the event's processing conversation).
func (s *Service) suggestFrom(ctx context.Context, sessionID string, in suggestIn) (string, *DocChange, *SessionRef, error) {
	if err := in.check(); err != nil {
		return "", nil, nil, err
	}
	if s.inboxReady(ctx) != nil {
		return "", nil, nil, errInboxUnavailable
	}
	b := &s.inbox
	b.mu.Lock()
	var it *InboxItem
	var from *SessionRef
	if item := strings.TrimSpace(in.Item); item != "" {
		var err error
		if it, from, err = b.handedItem(item, sessionID); err != nil {
			b.mu.Unlock()
			return "", nil, nil, err
		}
	} else {
		it = b.itemOfAgent(sessionID)
		// A conversation that answers before its start returned (an agent runtime already running starts a turn at
		// once): its item learns the conversation's id a moment later.
		for wait := time.Now().Add(inboxDraftWait); it == nil && b.agentStarting() && time.Now().Before(wait); {
			b.mu.Unlock()
			time.Sleep(100 * time.Millisecond)
			b.mu.Lock()
			it = b.itemOfAgent(sessionID)
		}
		if it == nil {
			b.mu.Unlock()
			return "", nil, nil, errNotProcessing
		}
	}
	for _, ch := range in.Changes {
		if it.Doc == nil || !docOffers(*it.Doc, ch) {
			b.mu.Unlock()
			return it.ID, &ch, nil, nil
		}
	}
	now := time.Now().UTC()
	w := beginWrite(it)
	if r := strings.TrimSpace(in.Recap); r != "" {
		it.Recap = clip(r, recapMax)
	}
	for _, id := range in.Related {
		relate(it, b.items[strings.TrimSpace(id)])
	}
	var card *MessageCard
	stop := false
	if from == nil && in.Kind == "" && it.open() && it.Handling != nil && it.Handling.State == HandlingLooking {
		// Only the recap: this turn changes nothing else, the event goes back to where it was.
		back := it.Agent.Back
		if it.Suggestion != nil || back == "" || back == HandlingLooking || back == HandlingFailed {
			back = HandlingDone
		}
		it.Agent.Wake = false
		b.setHandling(it, back, "", now)
	}
	if in.Kind != "" && it.open() {
		if legacySuggestion(it) != nil {
			it.SuggestSeq = max(it.SuggestSeq, 1)
		}
		it.SuggestSeq++
		g := &InboxSuggestion{Version: it.SuggestSeq, Kind: in.Kind, Text: in.Text, Note: clip(strings.TrimSpace(in.Note), 600),
			Handback: in.Handback, Changes: in.Changes, At: now, Cwd: strings.TrimSpace(in.Cwd), Conversation: from}
		if in.Kind == SuggestAgent {
			if m, ok := s.machineByName(ctx, strings.TrimSpace(in.Machine)); ok {
				g.MachineID = m.ID
			}
		}
		it.Suggestion = g
		var link *InboxChainLink
		if from == nil {
			it.Agent.Wake, it.Agent.Back = false, ""
			b.setHandling(it, HandlingDone, "", now)
			link = &InboxChainLink{Type: LinkSession, MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
		} else {
			clearDraft(it)
			link = &InboxChainLink{Type: LinkSession, MachineID: from.MachineID, SessionID: from.SessionID}
		}
		l := b.userLang()
		switch {
		case in.Kind == SuggestConclusion:
			b.chainAdd(it, InboxChainEntry{Kind: ChainConcluded, At: now, Text: i18n.T(l, "assistant-inbox_chain.concluded"), Detail: in.Text})
			if in.Outcome == "done" {
				it.Suggestion = nil
				b.file(it, now, doneByManager)
				if from != nil {
					stop = b.closeHandling(it, "read", now) // a processing conversation still on it stops
				}
			} else {
				it.Tier = TierFYI
			}
		default:
			key := "suggested_reply"
			if in.Handback {
				key = "suggested_handback"
			} else if in.Kind == SuggestAgent {
				key = "suggested_agent"
			}
			b.chainAdd(it, InboxChainEntry{Kind: ChainHandling, At: now, Text: i18n.T(l, "assistant-inbox_chain."+key), Detail: clip(in.Text, 600), Link: link})
		}
		if in.Kind == SuggestReply && from == nil {
			card = messageCardOf(it)
		}
	}
	id := it.ID
	b.settle(w)
	b.mu.Unlock()
	// An undone suggestion gets no card, and the conversation hears it failed: the inbox must not promise what a
	// restart would lose. Undone, the event is back where the turn found it, so handing it in again works. One that
	// something else moved meanwhile stays (that move's save writes the row) and goes on as kept.
	if s.saveWrites(ctx, []*inboxWrite{w}, "its suggestion"); w.undone {
		return id, nil, from, errInboxNotPersisted
	}
	if card != nil {
		s.messageCard(ctx, id, *card)
	}
	if stop {
		s.tellSoon(ctx, it)
	}
	return id, nil, from, nil
}

// inboxDraft keeps the reply drafted in conversation sessionID (inbox_draft, the older name): suggest with kind reply.
func (s *Service) inboxDraft(ctx context.Context, sessionID, text, note string, proposed []DocChange) (string, *DocChange, error) {
	id, refused, err := s.suggest(ctx, sessionID, suggestIn{Kind: SuggestReply, Text: text, Note: note, Changes: proposed})
	if err != nil && !errors.Is(err, errInboxNotPersisted) {
		return "", nil, nil
	}
	return id, refused, err
}

func (s *Service) toolInboxSuggest(ctx context.Context, self string, args json.RawMessage) (string, error) {
	var in suggestIn
	if err := json.Unmarshal(args, &in); err != nil {
		return "", fmt.Errorf("arguments: %v", err)
	}
	_, refused, from, err := s.suggestFrom(ctx, self, in)
	if err != nil {
		return "", err
	}
	if refused != nil {
		return "", fmt.Errorf("change %+v is not one the item lists (ref, field and one of its values)", *refused)
	}
	if in.Kind == "" {
		return "The recap is updated.", nil
	}
	if from != nil {
		return "The suggestion is in the user's inbox; nothing goes out until they confirm. What they do with it is told here; later messages on the item are not.", nil
	}
	return "The suggestion is in the user's inbox; nothing goes out until they confirm. End this turn: what comes next (their answer, new messages) is sent here.", nil
}

// inboxAgentStarted records the Agent an event's conversation started (agent_start from it): the chain links that
// sub-session, the event is In progress (Agent is working) until the conversation hands in what it found.
func (s *Service) inboxAgentStarted(ctx context.Context, self, machine, session, prompt string) {
	if self == "" || session == "" || s.inboxReady(ctx) != nil {
		return
	}
	if hs, ok := ctx.Value(handoffStartKey{}).(*handoffStart); ok {
		s.handoffAgentStarted(hs, machine, session, prompt)
		return
	}
	enabled := s.inboxConfig(ctx).Agent.Enabled
	b := &s.inbox
	b.mu.Lock()
	it := b.itemOfAgent(self)
	if it == nil || !it.open() {
		b.mu.Unlock()
		return
	}
	now := time.Now()
	b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent",
		i18n.Args{"title": clip(oneLine(prompt), 40)}), Link: &InboxChainLink{Type: LinkSession, MachineID: machine, SessionID: session}})
	if enabled && it.Suggestion == nil {
		t := now.UTC()
		it.Agent.StartedAt = &t
		b.setHandling(it, HandlingAgent, "", now)
	}
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
}

// legacySuggestion is an AI twin draft stored before suggestions, as a reply suggestion (version 1); nil for none.
func legacySuggestion(it *InboxItem) *InboxSuggestion {
	a := it.Agent
	if it.Suggestion != nil || it.Handling != nil || a == nil || a.State != AgentDrafted || strings.TrimSpace(a.Draft) == "" {
		return nil
	}
	g := &InboxSuggestion{Version: 1, Kind: SuggestReply, Text: a.Draft, Note: a.Note, Changes: a.Changes}
	g.At = it.UpdatedAt.UTC()
	if a.DraftedAt != nil {
		g.At = a.DraftedAt.UTC()
	}
	return g
}

// currentSuggestion is the item's suggestion as listed (an old draft's too).
func currentSuggestion(it *InboxItem) *InboxSuggestion {
	if it.Suggestion != nil {
		return it.Suggestion
	}
	return legacySuggestion(it)
}

// ---- POST /api/v1/inbox/{id}/suggestion ----

// Actions on a suggestion.
const (
	suggestConfirm  = "confirm"
	suggestReject   = "reject"
	suggestInstruct = "instruct"
)

type suggestionReq struct {
	Version  int     `json:"version"`
	Action   string  `json:"action"`
	Text     *string `json:"text"`
	ClientID string  `json:"client_id"`
}

var (
	errSuggestionStale = i18n.ErrEnglish("inbox-errors.suggestion_stale")
	errAnswerOnly      = i18n.ErrEnglish("inbox-errors.answer_only")
)

func (s *Service) inboxSuggestionRoutes() {
	s.handle("POST /api/v1/inbox/{id}/suggestion", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in suggestionReq
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		switch in.Action {
		case suggestConfirm, suggestReject:
		case suggestInstruct:
			if in.Text == nil || strings.TrimSpace(*in.Text) == "" {
				apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.text_required"))
				return
			}
		default:
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.suggestion_action"))
			return
		}
		it, err := s.actOnSuggestion(r.Context(), r.PathValue("id"), in, fullRole(r))
		l := i18n.OfWriter(w)
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errSuggestionStale):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "suggestion_stale", "error": i18n.T(l, "assistant-inbox.suggestion_stale"), "item": it})
		case errors.Is(err, errInboxItemDone):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "inbox_item_done", "error": i18n.T(l, "assistant-inbox.item_done"), "retryable": false, "delivered": "none"})
		case errors.Is(err, errAnswerOnly):
			apierr.Write(w, http.StatusForbidden, err)
		case errors.Is(err, errMailReplyTargetChanged):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "mail_reply_target_changed", "error": i18n.T(l, "assistant-inbox.mail_target_changed"), "retryable": false, "delivered": "none"})
		case replyInDoubt(err):
			body := apierr.LocalizedBody(http.StatusBadGateway, assistantwire.CodeAssistantInDoubt, err, i18n.OfWriter(w))
			body["delivered"], body["retryable"] = "unknown", false
			apierr.WriteBody(w, http.StatusBadGateway, body)
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		default:
			apierr.WriteJSON(w, it)
		}
	})
}

// actOnSuggestion confirms, rejects or answers the item's current suggestion, at the version the user saw. full is
// false for an answer-only device (the watch): it may only confirm a reply or a conclusion, as text.
func (s *Service) actOnSuggestion(ctx context.Context, id string, in suggestionReq, full bool) (InboxItem, error) {
	enabled := s.inboxConfig(ctx).Agent.Enabled
	b := &s.inbox
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.mu.Unlock()
		return InboxItem{}, errNoInboxItem
	}
	g := currentSuggestion(it)
	switch {
	case !it.open():
		b.mu.Unlock()
		return InboxItem{}, errInboxItemDone
	case g == nil || g.Version != in.Version:
		out := it.listed()
		b.mu.Unlock()
		return out, errSuggestionStale
	}
	text := g.Text
	if in.Text != nil && strings.TrimSpace(*in.Text) != "" {
		text = strings.TrimSpace(*in.Text)
	}
	if !full && (in.Action != suggestConfirm || g.Kind == SuggestAgent || g.Handback || len(g.Changes) > 0 || assistantwire.AsksAgent(text)) {
		b.mu.Unlock()
		return InboxItem{}, errAnswerOnly
	}
	if g.Conversation != nil {
		return s.actOnHandedSuggestion(ctx, it, g, in, text) // it lets b.mu go
	}
	w := beginWrite(it)
	if it.Agent == nil { // an old draft's item: its conversation's bookkeeping starts here
		it.Agent = &InboxAgent{State: AgentSkipped}
	}
	now := time.Now()
	l := b.userLang()
	edited := text != g.Text
	// The suggestion is taken now, under the lock: a second confirmation of it (another device, a retry) is stale.
	it.Suggestion = nil
	if it.Agent.State == AgentDrafted {
		it.Agent.State = AgentSkipped
	}
	execute := in.Action == suggestConfirm && !g.Handback
	switch {
	case in.Action == suggestReject:
		b.chainAdd(it, rejectedEntry(l, g.Text, now))
		b.setHandling(it, HandlingLooking, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteRejected, Text: g.Text}, true, now)
	case in.Action == suggestInstruct:
		words := strings.TrimSpace(*in.Text)
		b.chainAdd(it, instructedEntry(l, words, now))
		b.setHandling(it, HandlingLooking, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteInstructed, Text: words}, true, now)
	case !execute: // handback: the conversation does it
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
		b.setHandling(it, HandlingLooking, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteHandback, Text: text, Edited: edited}, true, now)
	case g.Kind == SuggestConclusion:
		b.chainAdd(it, confirmedEntry(l, text, false, now))
		b.setHandling(it, HandlingDone, "", now)
		b.file(it, now, "user")
	default:
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
	}
	if !enabled && !execute {
		b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now)
		b.settle(w)
	}
	last := it.Last
	b.mu.Unlock()
	if !execute || g.Kind == SuggestConclusion {
		if !enabled && !execute {
			if failed := s.saveWrites(ctx, []*inboxWrite{w}, "its suggestion response"); failed[it.ID] {
				return InboxItem{}, errInboxNotPersisted
			}
		} else {
			s.saveInbox(ctx, []*InboxItem{it})
		}
		if enabled && it.Agent != nil && len(it.Agent.Pending) > 0 {
			s.tellSoon(ctx, it)
		}
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		return out, nil
	}
	if g.Kind == SuggestAgent {
		return s.confirmAgent(ctx, it, g, text, edited)
	}
	return s.confirmReply(ctx, it, g, text, edited, last, in.ClientID)
}

func confirmedEntry(l i18n.Lang, text string, edited bool, now time.Time) InboxChainEntry {
	key := "assistant-inbox_chain.confirmed"
	if edited {
		key = "assistant-inbox_chain.confirmed_edited"
	}
	return InboxChainEntry{Kind: ChainConfirmed, At: now.UTC(), Text: i18n.T(l, key), Detail: text, Edited: edited}
}

func rejectedEntry(l i18n.Lang, text string, now time.Time) InboxChainEntry {
	return InboxChainEntry{Kind: ChainRejected, At: now.UTC(), Text: i18n.T(l, "assistant-inbox_chain.rejected"), Detail: clip(text, 600)}
}

func instructedEntry(l i18n.Lang, words string, now time.Time) InboxChainEntry {
	return InboxChainEntry{Kind: ChainInstructed, At: now.UTC(), Text: i18n.T(l, "assistant-inbox_chain.instructed",
		i18n.Args{"text": clip(oneLine(words), 40)}), Detail: words}
}

// confirmReply sends the confirmed reply as shown (or as the user edited it) through the same once-only path as
// /reply, but keeps the event open: it waits for the other side (handling waiting), and the conversation is told.
// A send that fails leaves the event Needs you without a suggestion.
func (s *Service) confirmReply(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool, last, clientID string) (InboxItem, error) {
	if !validClientID(clientID) { // the same suggestion confirmed again is the same send
		clientID = namedClientID(fmt.Sprintf("suggestion\n%s\n%d", it.ID, g.Version))
	}
	_, err := s.replyInboxDo(ctx, it.ID, text, clientID, false, false, g.Changes...)
	b := &s.inbox
	now := time.Now()
	b.mu.Lock()
	if err != nil {
		b.failHandling(it, i18n.T(b.userLang(), "assistant-inbox_chain.failed_send"), now)
	} else {
		it.Agent.SentOf = last
		b.setHandling(it, HandlingWaiting, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteSent, Text: text, Edited: edited}, false, now)
	}
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellSoon(ctx, it)
	}
	return out, err
}

// confirmAgent starts the Agent with the confirmed instruction, as the event's conversation would (agent_start from
// it: the chain links the sub-session, the conversation is told when it is done).
func (s *Service) confirmAgent(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	self := it.Agent.SessionID
	b.mu.Unlock()
	machine := g.MachineID
	if machine == "" {
		machine = s.settings(ctx).MachineID
	}
	cwd := g.Cwd
	if cwd == "" {
		cwd = "~"
	}
	args, _ := json.Marshal(map[string]string{"machine": machine, "cwd": cwd, "prompt": text})
	res, err := s.runTool(ctx, s.settings(ctx).MachineID, self, "agent_start", args)
	enabled := s.inboxConfig(ctx).Agent.Enabled
	now := time.Now()
	b.mu.Lock()
	if err != nil {
		b.failHandling(it, i18n.T(b.userLang(), "assistant-inbox_chain.failed_agent"), now)
	} else {
		session := ""
		for _, e := range it.Chain {
			if e.Kind == ChainAgent && e.Link != nil {
				session = e.Link.SessionID
			}
		}
		if self == "" { // no conversation recorded the start: the chain still links it
			b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent",
				i18n.Args{"title": clip(oneLine(text), 40)})})
		}
		b.setHandling(it, HandlingAgent, "", now)
		b.tell(it, inboxNote{Name: prompts.NoteAgentStarted, Text: text, Edited: edited, Machine: machine, Session: session}, false, now)
		_ = res
	}
	if !enabled {
		b.releaseInboxAgent(it, HandlingFailed, i18n.T(b.userLang(), "assistant-inbox_agent.disabled"), now)
	}
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellSoon(ctx, it)
	}
	return out, err
}

// linkTask asks the judge (model 2) which of the user's tasks going on the event is about (docs/assistant/unified-entry.md):
// the conversation is told that task's progress, and its reply suggestion puts a card in the task's conversation.
// Since phase 1 every Needs you event goes to the manager whatever the judge says of handling it; a model that fails
// leaves the event unlinked.
func (s *Service) linkTask(ctx context.Context, it *InboxItem, tasks []prompts.InboxTask) bool {
	return s.linkTaskFor(ctx, it, tasks, nil)
}

// linkTaskFor is linkTask on an item whose write w is not saved yet: the link joins that write while the item still
// holds it (a concurrent move keeps the write from being undone over it).
func (s *Service) linkTaskFor(ctx context.Context, it *InboxItem, tasks []prompts.InboxTask, w *inboxWrite) bool {
	b := &s.inbox
	b.mu.Lock()
	input := prompts.RenderInboxJudgeInput(prompts.InboxJudgeInput{Summary: it.Summary, Item: b.promptItem(it), Tasks: tasks})
	b.mu.Unlock()
	system := prompts.RenderInboxJudge(prompts.InboxJudge{Owner: s.owner(ctx), Rules: s.inboxConfig(ctx).AgentRules})
	var out struct {
		TaskID string `json:"task_id"`
	}
	cctx, cancel := context.WithTimeout(ctx, 90*time.Second)
	err := s.inboxModelJSON(cctx, purposeJudge, system, input, 0, &out)
	cancel()
	if err != nil {
		if paused, _ := modelPaused(err); ctx.Err() == nil && !paused {
			s.logf("inbox: link %s to a task: %v", it.ID, err)
		}
		return false
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, t := range tasks { // only a task it was shown
		if it.Agent != nil && t.ID == strings.TrimSpace(out.TaskID) {
			held := w != nil && b.holds(w)
			it.Agent.TodoID = t.ID
			if held {
				b.settle(w)
			}
			return true
		}
	}
	return false
}
