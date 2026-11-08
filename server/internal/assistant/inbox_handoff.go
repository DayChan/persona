package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Hand to AI with write-back (docs/assistant/inbox.md "Item actions", #1194): the user hands an item to Persona's main
// conversation, which may then write back to it with inbox_suggest (item). The item keeps which conversations it was
// handed to (Handoffs); its processing conversation (Agent, Handling) is not touched, so the AI twin's sweep and
// timeouts never take the main conversation for one of its own. What the user does with a suggestion from there is
// told there (tellHandoff), not to the processing conversation. Later messages on the item are not forwarded to it.

// handoffsMax is how many hand-offs an item keeps, the latest.
const handoffsMax = 5

type handoffReq struct {
	Text     string `json:"text"`
	ClientID string `json:"client_id"`
}

func (s *Service) inboxHandoffRoutes() {
	s.handle("POST /api/v1/inbox/{id}/handoff", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in handoffReq
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		if strings.TrimSpace(in.Text) == "" {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.text_required"))
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), 110*time.Second) // a main conversation may have to start
		defer cancel()
		it, to, err := s.handOff(ctx, r.PathValue("id"), in)
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case err != nil:
			apierr.Call(w, err)
		default:
			apierr.WriteJSON(w, map[string]any{"item": it, "session": to})
		}
	})
}

// handOff sends the client's text, after a block naming the item, to the main conversation (or starts it with it)
// and records the hand-off. The same request again (its client id, or the item and text) sends nothing more: one
// already recorded is answered as it was. A repeat whose record was lost is dropped by session.send_once when the
// main conversation was there already; one that started it starts another.
func (s *Service) handOff(ctx context.Context, id string, in handoffReq) (InboxItem, SessionRef, error) {
	b := &s.inbox
	text := strings.TrimSpace(in.Text)
	op := strings.ToLower(strings.TrimSpace(in.ClientID))
	if !validClientID(op) {
		op = namedClientID("inbox handoff\n" + id + "\n" + text)
	}
	v, _ := b.handingOff.LoadOrStore(id, &sync.Mutex{})
	mu := v.(*sync.Mutex)
	mu.Lock()
	defer mu.Unlock()
	b.mu.Lock()
	it := b.items[id]
	if it == nil {
		b.handingOff.CompareAndDelete(id, mu)
		b.mu.Unlock()
		return InboxItem{}, SessionRef{}, errNoInboxItem
	}
	if h := handoffOp(it, op); h != nil {
		out, to := it.listed(), SessionRef{MachineID: h.MachineID, SessionID: h.SessionID}
		b.mu.Unlock()
		return out, to, nil
	}
	title := clip(oneLine(it.Title), 80)
	b.mu.Unlock()
	to, err := s.sendHandoff(ctx, prompts.RenderInboxHandoff(prompts.InboxHandoff{ID: id, Title: title, Text: text}), op)
	if err != nil {
		return InboxItem{}, SessionRef{}, err
	}
	b.mu.Lock()
	b.noteHandoff(it, to, op, time.Now())
	out := it.listed()
	b.mu.Unlock()
	// No rollback: the message is in the conversation. The record stays in memory and the item's next save writes it.
	if failed := s.saveInbox(ctx, []*InboxItem{it}); failed[id] {
		s.logf("inbox: %s was handed to %s but could not be saved; the live state stays until the next save", id, short(to.SessionID))
	}
	return out, to, nil
}

// sendHandoff sends text to the main conversation once per op (session.send_once, the client's message id), or
// starts the main conversation with it when there is none.
func (s *Service) sendHandoff(ctx context.Context, text, op string) (SessionRef, error) {
	cfg := s.settings(ctx)
	if cfg.Main == "" || cfg.MachineID == "" {
		sess, err := s.Start(ctx, StartParams{Text: s.withWorkspaces(ctx, text), Main: true}) // as SendParams does for the next messages
		if err != nil {
			return SessionRef{}, err
		}
		return SessionRef{MachineID: sess.MachineID, SessionID: sess.SessionID}, nil
	}
	to := SessionRef{MachineID: cfg.MachineID, SessionID: cfg.Main}
	if s.isWorkMachine(to.MachineID) {
		return SessionRef{}, errWorkAssistant
	}
	s.UserSpoke(ctx, to) // unarchived, and its title the user's to keep
	params, err := s.SendParams(ctx, SendParams{MachineID: to.MachineID, SessionID: to.SessionID,
		Params: map[string]any{"session_id": to.SessionID, "text": text, "client_msg_id": op}})
	if err != nil {
		return SessionRef{}, err
	}
	if err := s.sendOnce(ctx, to.MachineID, params); err != nil {
		return SessionRef{}, err
	}
	return to, nil
}

// noteHandoff records that conversation to got the item (op: the request's client id), keeping the latest
// handoffsMax, and puts it on the chain once per conversation. Under b.mu.
func (b *inboxBook) noteHandoff(it *InboxItem, to SessionRef, op string, now time.Time) {
	it.Handoffs = append(it.Handoffs, InboxHandoff{MachineID: to.MachineID, SessionID: to.SessionID, At: now.UTC(), Op: op})
	if n := len(it.Handoffs); n > handoffsMax {
		it.Handoffs = slices.Clone(it.Handoffs[n-handoffsMax:])
	}
	for _, e := range it.Chain {
		if e.Kind == ChainHandling && e.Link != nil && e.Link.SessionID == to.SessionID {
			return
		}
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainHandling, At: now.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.handed_main"),
		Link: &InboxChainLink{Type: LinkSession, MachineID: to.MachineID, SessionID: to.SessionID}})
}

// handoffOp is the item's hand-off made by request op; nil for none. Under b.mu.
func handoffOp(it *InboxItem, op string) *InboxHandoff {
	for i := range it.Handoffs {
		if it.Handoffs[i].Op == op {
			return &it.Handoffs[i]
		}
	}
	return nil
}

// handoffTo is the item's latest hand-off to conversation sid; nil for none. Under b.mu.
func handoffTo(it *InboxItem, sid string) *InboxHandoff {
	for i := len(it.Handoffs) - 1; i >= 0 && sid != ""; i-- {
		if it.Handoffs[i].SessionID == sid {
			return &it.Handoffs[i]
		}
	}
	return nil
}

// listedHandoffs are the hand-offs as listed, without their op.
func listedHandoffs(hs []InboxHandoff) []InboxHandoff {
	if len(hs) == 0 {
		return nil
	}
	out := slices.Clone(hs)
	for i := range out {
		out[i].Op = ""
	}
	return out
}

// handedItem is the item inbox_suggest's item names for conversation sid, under b.mu: the item of its own processing
// conversation, as without item (from nil), or one handed to sid (from: that conversation). A refusal is said so the
// model answers in the chat instead.
func (b *inboxBook) handedItem(id, sid string) (*InboxItem, *SessionRef, error) {
	it := b.items[id]
	switch {
	case it == nil:
		return nil, nil, errSuggestion{fmt.Sprintf("there is no inbox item %q (it may have been cleaned up): answer in the chat instead", id)}
	case sid != "" && it.Agent != nil && it.Agent.SessionID == sid:
		return it, nil, nil
	}
	h := handoffTo(it, sid)
	switch {
	case h == nil:
		return nil, nil, errSuggestion{fmt.Sprintf("inbox item %q was not handed to this conversation: only a conversation it was handed to can write back to it", id)}
	case !it.open():
		return nil, nil, errSuggestion{fmt.Sprintf("the user already filed inbox item %q, so nothing was changed: answer in the chat instead", id)}
	}
	return it, &SessionRef{MachineID: h.MachineID, SessionID: h.SessionID}, nil
}

// clearDraft takes an earlier reply suggestion's mirror off the item's Agent (the old apps' draft): a hand-off
// conversation's suggestion replaced it. Under b.mu.
func clearDraft(it *InboxItem) {
	if a := it.Agent; a != nil && a.State == AgentDrafted {
		a.State, a.Draft, a.Note, a.Changes = AgentSkipped, "", "", nil
	}
}

// renderHandoffNote is a handling note for a conversation the item was handed to, wrapped to name the item.
func renderHandoffNote(n inboxNote, owner, id, title string) string {
	if title == "" { // the head the hub recognizes needs a title (prompts.ParseInjected)
		title = id
	}
	note := prompts.RenderInboxHandlingNote(n.Name, prompts.InboxHandlingNote{Owner: owner, Text: n.Text, Edited: n.Edited,
		Context: n.Context, Machine: n.Machine, Session: n.Session, Handoff: true})
	return prompts.RenderInboxHandoffNote(prompts.InboxHandoffNote{ID: id, Title: title, Note: note})
}

// tellHandoff tells the conversation a suggestion came from what the user did with it, in the background (a request
// answers first). It is a Persona notice, not the user's words: a plain session.send without a message id, which the
// hub shows the apps as one line (prompts.ParseInjected). A failure is only logged.
func (s *Service) tellHandoff(ctx context.Context, id, title string, to SessionRef, n inboxNote) {
	ctx = context.WithoutCancel(ctx)
	go func() {
		if s.isWorkMachine(to.MachineID) {
			s.logf("inbox: tell %s about %s: a work machine", short(to.SessionID), id)
			return
		}
		cctx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		text := renderHandoffNote(n, s.owner(cctx), id, title)
		params, err := s.SendParams(cctx, SendParams{MachineID: to.MachineID, SessionID: to.SessionID,
			Params: map[string]any{"session_id": to.SessionID, "text": text}})
		if err == nil {
			err = s.call(cctx, to.MachineID, DeviceSessionSend, params, nil)
		}
		if err != nil {
			s.logf("inbox: tell %s about %s: %v", short(to.SessionID), id, err)
		}
	}()
}

// handoffStart names the item an agent_start runs for on behalf of a conversation it was handed to
// (confirmHandedAgent); inboxAgentStarted puts the started Agent on that item's chain and its session here.
type handoffStart struct {
	item, session string
}

type handoffStartKey struct{}

// handoffAgentStarted is inboxAgentStarted for an agent_start made for a hand-off conversation's suggestion: the
// chain links the sub-session; the item's processing conversation is not touched.
func (s *Service) handoffAgentStarted(hs *handoffStart, machine, session, prompt string) {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := b.items[hs.item]
	if it == nil || !it.open() {
		return
	}
	hs.session = session
	b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: time.Now().UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain.agent",
		i18n.Args{"title": clip(oneLine(prompt), 40)}), Link: &InboxChainLink{Type: LinkSession, MachineID: machine, SessionID: session}})
}

// actOnHandedSuggestion is actOnSuggestion for a suggestion a conversation the event was handed to handed in
// (g.Conversation): what the user does with it is told there, never to the event's processing conversation, whose
// state stays as it is. A confirmed reply files the event as replied, as /reply does: nobody here follows the other
// side's answer. Called with b.mu held; it lets it go.
func (s *Service) actOnHandedSuggestion(ctx context.Context, it *InboxItem, g *InboxSuggestion, in suggestionReq, text string) (InboxItem, error) {
	b := &s.inbox
	now := time.Now()
	l := b.userLang()
	edited := text != g.Text
	to, title := *g.Conversation, clip(oneLine(it.Title), 80)
	it.Suggestion = nil
	execute := in.Action == suggestConfirm && !g.Handback && g.Kind != SuggestConclusion
	var note *inboxNote
	closed := false
	switch {
	case in.Action == suggestReject:
		b.chainAdd(it, rejectedEntry(l, g.Text, now))
		note = &inboxNote{Name: prompts.NoteRejected, Text: g.Text}
	case in.Action == suggestInstruct:
		words := strings.TrimSpace(*in.Text)
		b.chainAdd(it, instructedEntry(l, words, now))
		note = &inboxNote{Name: prompts.NoteInstructed, Text: words}
	case g.Handback:
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
		note = &inboxNote{Name: prompts.NoteHandback, Text: text, Edited: edited}
	case g.Kind == SuggestConclusion:
		b.chainAdd(it, confirmedEntry(l, text, false, now))
		b.file(it, now, "user")
		closed = b.closeHandling(it, "user", now) // a processing conversation still on it stops
	default:
		b.chainAdd(it, confirmedEntry(l, text, edited, now))
	}
	b.mu.Unlock()
	if !execute {
		s.saveInbox(ctx, []*InboxItem{it})
		if closed {
			s.tellSoon(ctx, it)
		}
		if note != nil {
			s.tellHandoff(ctx, it.ID, title, to, *note)
		}
		b.mu.Lock()
		out := it.listed()
		b.mu.Unlock()
		return out, nil
	}
	if g.Kind == SuggestAgent {
		return s.confirmHandedAgent(ctx, it, g, text, edited, to, title)
	}
	return s.confirmHandedReply(ctx, it, g, text, edited, in.ClientID, to, title)
}

// confirmHandedReply sends a hand-off conversation's confirmed reply through /reply's once-only path and files the
// event; the conversation is told it went out. A send that fails puts that on the chain.
func (s *Service) confirmHandedReply(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool, clientID string,
	to SessionRef, title string) (InboxItem, error) {
	if !validClientID(clientID) { // the same suggestion confirmed again is the same send
		clientID = namedClientID(fmt.Sprintf("suggestion\n%s\n%d", it.ID, g.Version))
	}
	out, err := s.replyInboxDo(ctx, it.ID, text, clientID, false, true, g.Changes...)
	b := &s.inbox
	if err != nil {
		b.mu.Lock()
		l := b.userLang()
		b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: time.Now().UTC(), Text: i18n.T(l, "assistant-inbox_chain.failed"),
			Detail: i18n.T(l, "assistant-inbox_chain.failed_send")})
		out = it.listed()
		b.mu.Unlock()
		s.saveInbox(ctx, []*InboxItem{it})
		return out, err
	}
	b.mu.Lock()
	open := it.open()
	b.mu.Unlock()
	if open { // new messages came in while it was out: the reply's own save left the item as it was
		s.saveInbox(ctx, []*InboxItem{it})
	}
	s.tellHandoff(ctx, it.ID, title, to, inboxNote{Name: prompts.NoteSent, Text: text, Edited: edited})
	return out, nil
}

// confirmHandedAgent starts the Agent of a hand-off conversation's confirmed suggestion as that conversation would
// (agent_start from it: it is that conversation's task, and is told there when it is done); the chain links the
// sub-session.
func (s *Service) confirmHandedAgent(ctx context.Context, it *InboxItem, g *InboxSuggestion, text string, edited bool, to SessionRef,
	title string) (InboxItem, error) {
	machine := g.MachineID
	if machine == "" {
		machine = s.settings(ctx).MachineID
	}
	cwd := g.Cwd
	if cwd == "" {
		cwd = "~"
	}
	args, _ := json.Marshal(map[string]string{"machine": machine, "cwd": cwd, "prompt": text})
	hs := &handoffStart{item: it.ID}
	_, err := s.runTool(context.WithValue(ctx, handoffStartKey{}, hs), s.settings(ctx).MachineID, to.SessionID, "agent_start", args)
	b := &s.inbox
	b.mu.Lock()
	l, now := b.userLang(), time.Now().UTC()
	switch {
	case err != nil:
		b.chainAdd(it, InboxChainEntry{Kind: ChainFailed, At: now, Text: i18n.T(l, "assistant-inbox_chain.failed"),
			Detail: i18n.T(l, "assistant-inbox_chain.failed_agent")})
	case hs.session == "": // the start's answer was lost on the way: the chain still says it started
		b.chainAdd(it, InboxChainEntry{Kind: ChainAgent, At: now, Text: i18n.T(l, "assistant-inbox_chain.agent",
			i18n.Args{"title": clip(oneLine(text), 40)})})
	}
	session := hs.session
	out := it.listed()
	b.mu.Unlock()
	s.saveInbox(ctx, []*InboxItem{it})
	if err == nil {
		s.tellHandoff(ctx, it.ID, title, to, inboxNote{Name: prompts.NoteAgentStarted, Text: text, Edited: edited,
			Machine: machine, Session: session})
	}
	return out, err
}
