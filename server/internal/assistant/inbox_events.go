package assistant

import (
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Inbox events, phase 1 (docs/assistant/inbox.md "Events and handling chain"): an item is an event. Besides what it always had it
// keeps its handling chain (chain: the key calls in time order, every one recorded by the program where it happens, never
// written by a model), whether it is urgent, the manager's recap, the processing conversation bound to it (handling),
// at most one suggestion, and the items it is related to across atoms. Its section (status) is computed from those.

// Item statuses (InboxItem.Status, listed for open items only).
const (
	StatusAction  = "action"  // "Needs you"
	StatusWorking = "working" // "In progress"
	StatusFYI     = "fyi"     // "FYI"
	StatusNoise   = "noise"   // "Folded"
)

// Chain entry kinds (InboxChainEntry.Kind).
const (
	ChainReceived    = "received"
	ChainTriaged     = "triaged"
	ChainHandling    = "handling"
	ChainSent        = "sent"
	ChainAgent       = "agent"
	ChainConfirmed   = "confirmed"
	ChainRejected    = "rejected"
	ChainInstructed  = "instructed"
	ChainSelfReplied = "self_replied"
	ChainFailed      = "failed"
	ChainConcluded   = "concluded"
	ChainDone        = "done" // filed (by the user, read elsewhere, the manager): not in the contract's table, drawn generically
)

const (
	chainMax         = 50  // entries kept, the newest
	chainMessagesMax = 10  // messages a received entry carries
	chainMessageText = 200 // characters of one
	recapMax         = 600
)

// Link types of a chain entry.
const (
	LinkSource  = "source"
	LinkSession = "session"
	LinkItem    = "item"
)

// InboxChainEntry is one key call on an event.
type InboxChainEntry struct {
	ID       string              `json:"id"`
	At       time.Time           `json:"at"`
	Kind     string              `json:"kind"`
	Text     string              `json:"text"`
	Edited   bool                `json:"edited,omitempty"`
	Detail   string              `json:"detail,omitempty"`
	Count    int                 `json:"count,omitempty"`
	Sender   string              `json:"sender,omitempty"`
	Messages []InboxChainMessage `json:"messages,omitempty"`
	Link     *InboxChainLink     `json:"link,omitempty"`
}

// InboxChainMessage is one message of a received batch.
type InboxChainMessage struct {
	Sender string     `json:"sender,omitempty"`
	Text   string     `json:"text"`
	At     *time.Time `json:"at,omitempty"`
}

// InboxChainLink is where tapping an entry goes.
type InboxChainLink struct {
	Type      string `json:"type"`
	Position  string `json:"position,omitempty"`
	UID       uint32 `json:"uid,omitempty"`
	URL       string `json:"url,omitempty"`
	MachineID string `json:"machine_id,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	ID        string `json:"id,omitempty"`
}

// InboxRelated is another item related to this one across atoms (another chat, another source): listed so the apps
// jump to it; each goes its own way.
type InboxRelated struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Source string `json:"source"`
}

// chainAdd appends an entry stamped with the next id, keeping the newest chainMax. An item from before chains (none
// yet, but messages and a tier) gets its two synthesized entries first, so they don't vanish. The item changed, so
// GET /api/v1/inbox encodes it again (inbox_list_memo.go): many changes put an entry here and make a call before they
// save, or never save (a reply kept open). Under b.mu.
func (b *inboxBook) chainAdd(it *InboxItem, e InboxChainEntry) {
	delete(b.listMemo.kept, it.ID)
	if len(it.Chain) == 0 && e.Kind != ChainReceived {
		it.Chain = synthesizedChain(b.userLang(), it)
		it.ChainSeq = len(it.Chain)
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	it.ChainSeq++
	e.ID = "c" + strconv.Itoa(it.ChainSeq)
	it.Chain = append(it.Chain, e)
	if n := len(it.Chain); n > chainMax {
		it.Chain = slices.Clone(it.Chain[n-chainMax:])
	}
}

// synthesizedChain is what an item from before chains shows: received and triaged, from its fields.
func synthesizedChain(l i18n.Lang, it *InboxItem) []InboxChainEntry {
	if it.Count == 0 && it.Last == "" {
		return nil
	}
	recv := InboxChainEntry{ID: "c1", At: it.StartedAt.UTC(), Kind: ChainReceived, Count: max(it.Count, 1), Sender: it.Sender,
		Link: firstLink(it)}
	recv.Text = receivedText(l, it, recv.Count, recv.Sender)
	out := []InboxChainEntry{recv}
	if it.Tier != "" {
		at := it.UpdatedAt.UTC()
		if at.Before(recv.At) {
			at = recv.At
		}
		t := triagedEntry(l, it)
		t.ID, t.At = "c2", at
		out = append(out, t)
	}
	return out
}

// listedChain is the chain as listed: an item from before chains gets its synthesized entries (loadInbox gives
// them in the user's language; one made since always has a received entry). A conclusion has no link (it expands
// inline): one stored with the conversation's link before is listed without it.
func listedChain(it *InboxItem) []InboxChainEntry {
	if len(it.Chain) == 0 {
		return synthesizedChain(i18n.Default, it)
	}
	out := slices.Clone(it.Chain)
	for i := range out {
		if out[i].Kind == ChainConcluded {
			out[i].Link = nil
		}
	}
	return out
}

func tierWord(l i18n.Lang, tier string) string {
	switch tier {
	case TierFYI, TierNoise:
		return i18n.T(l, "assistant-inbox_chain.tier_"+tier)
	}
	return i18n.T(l, "assistant-inbox_chain.tier_action")
}

func triagedEntry(l i18n.Lang, it *InboxItem) InboxChainEntry {
	tier := tierWord(l, it.Tier)
	key := "assistant-inbox_chain.triaged"
	if it.Urgent && it.Tier == TierAction {
		key = "assistant-inbox_chain.triaged_urgent"
	}
	e := InboxChainEntry{Kind: ChainTriaged, Text: i18n.T(l, key, i18n.Args{"tier": tier}), Detail: tier}
	if r := strings.TrimSpace(it.Reason); r != "" {
		e.Detail = i18n.T(l, "assistant-inbox_chain.triaged_detail", i18n.Args{"tier": tier, "reason": r})
	}
	return e
}

// noteTriaged records that the item was sorted at its current content. A re-triage that lands on the same tier (and
// urgency) adds nothing: the chain holds key calls, not every pass. Under b.mu.
func (b *inboxBook) noteTriaged(it *InboxItem) {
	for i := len(it.Chain) - 1; i >= 0; i-- {
		e := it.Chain[i]
		if e.Kind == ChainTriaged {
			if e.Text == triagedEntry(b.userLang(), it).Text {
				return
			}
			break
		}
	}
	b.chainAdd(it, triagedEntry(b.userLang(), it))
}

func receivedText(l i18n.Lang, it *InboxItem, count int, sender string) string {
	key := "assistant-inbox_chain.received"
	if it.Mail != nil {
		key = "assistant-inbox_chain.received_mail"
	}
	if sender != "" {
		key += "_from"
	}
	return i18n.T(l, key, i18n.Args{"count": count, "sender": sender})
}

// firstLink is where the item's first message is in its source.
func firstLink(it *InboxItem) *InboxChainLink {
	switch {
	case it.Lark != nil && it.Lark.StartPosition != "":
		return &InboxChainLink{Type: LinkSource, Position: it.Lark.StartPosition}
	case it.Mail != nil && len(it.Mail.UIDs) > 0:
		return &InboxChainLink{Type: LinkSource, UID: it.Mail.UIDs[0]}
	}
	return sourceLink(it)
}

// sourceLink is the item's own source page, at its URL when it has one.
func sourceLink(it *InboxItem) *InboxChainLink {
	l := &InboxChainLink{Type: LinkSource}
	switch {
	case it.Connector != nil && it.Connector.URL != "":
		l.URL = it.Connector.URL
	case it.Doc != nil && it.Doc.URL != "":
		l.URL = it.Doc.URL
	}
	return l
}

// inboxSeen is what an item held before a collection moved it: received entries say what came in since.
type inboxSeen struct {
	last  string
	count int
	uids  []uint32
}

func seenOf(it *InboxItem) inboxSeen {
	s := inboxSeen{last: it.Last, count: it.Count}
	if it.Mail != nil {
		s.uids = slices.Clone(it.Mail.UIDs)
	}
	return s
}

// noteReceived records the batch that just came into the item (its Last moved from prev's): one entry a batch, with
// how many, who, the messages when they can be told apart, and where the batch's first message is. messages, when
// the caller has them (mail), are the batch's; else they are read from the item. Under b.mu.
func (b *inboxBook) noteReceived(it *InboxItem, prev inboxSeen, messages []InboxChainMessage) {
	if it.Last == prev.last && prev.last != "" {
		return
	}
	fresh := prev.last == "" && len(it.Chain) == 0
	n := it.Count - prev.count
	if fresh {
		n = it.Count
	}
	n = max(n, 1)
	e := InboxChainEntry{Kind: ChainReceived, Count: n, Sender: it.Sender, At: it.UpdatedAt.UTC()}
	if e.At.IsZero() || e.At.After(time.Now().Add(time.Minute)) {
		e.At = time.Now().UTC()
	}
	if len(it.Chain) > 0 && e.At.Before(it.Chain[len(it.Chain)-1].At) {
		e.At = it.Chain[len(it.Chain)-1].At
	}
	e.Text = receivedText(b.userLang(), it, n, e.Sender)
	switch {
	case it.Lark != nil:
		e.Link = &InboxChainLink{Type: LinkSource, Position: batchStart(it, prev, n, fresh)}
	case it.Mail != nil:
		e.Link = &InboxChainLink{Type: LinkSource}
		for _, uid := range it.Mail.UIDs {
			if !slices.Contains(prev.uids, uid) {
				e.Link.UID = uid
				break
			}
		}
		if e.Link.UID == 0 && len(it.Mail.UIDs) > 0 {
			e.Link.UID = it.Mail.UIDs[len(it.Mail.UIDs)-1]
		}
	default:
		e.Link = sourceLink(it)
	}
	if messages == nil {
		messages = contextMessages(it.Context, n)
	}
	if len(messages) == 0 && strings.TrimSpace(it.Preview) != "" && n == 1 {
		messages = []InboxChainMessage{{Sender: it.Sender, Text: clip(oneLine(it.Preview), chainMessageText)}}
	}
	if len(messages) > chainMessagesMax {
		messages = messages[len(messages)-chainMessagesMax:]
	}
	e.Messages = messages
	b.chainAdd(it, e)
}

// batchStart is the Feishu position of a batch's first message: the item's start for its first batch; else, the
// positions being a chat's running number, the one n before the newest (never at or before what was there), else
// the message the batch follows.
func batchStart(it *InboxItem, prev inboxSeen, n int, fresh bool) string {
	if fresh && it.Lark.StartPosition != "" {
		return it.Lark.StartPosition
	}
	last, err1 := strconv.ParseInt(it.Last, 10, 64)
	before, err2 := strconv.ParseInt(prev.last, 10, 64)
	if err1 == nil && err2 == nil && last > before {
		return strconv.FormatInt(max(last-int64(n)+1, before+1), 10)
	}
	if prev.last != "" {
		return prev.last
	}
	return it.Last
}

// contextLine is a message line of a chat's context as the Feishu plugin writes it: "[01-02 15:04] <name>(@-mentioned me): ...",
// with the full-width parentheses and colon matched below.
var contextLine = lazyre.New(`^\[[^\]]{1,20}\] ([^：]{1,60}?)(（@了我）)?：(.*)$`)

// contextMessages are the newest n message lines of a chat's context; nil when its lines don't read as messages.
func contextMessages(context string, n int) []InboxChainMessage {
	if context == "" || n <= 0 {
		return nil
	}
	var out []InboxChainMessage
	for _, line := range strings.Split(context, "\n") {
		if m := contextLine.FindStringSubmatch(strings.TrimSpace(line)); m != nil {
			out = append(out, InboxChainMessage{Sender: m[1], Text: clip(strings.TrimSpace(m[3]), chainMessageText)})
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out[max(len(out)-n, 0):]
}

// file files an item (done_by by) and records it on the chain. Under b.mu.
func (b *inboxBook) file(it *InboxItem, at time.Time, by string) {
	it.done(at, by)
	key := ""
	switch by {
	case "user":
		key = "done_user"
	case "dismissed":
		key = "done_dismissed"
	case "read":
		key = "done_read"
	case doneByManager:
		key = "done_manager"
	}
	if key != "" {
		b.chainAdd(it, InboxChainEntry{Kind: ChainDone, At: at.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain."+key)})
	}
}

// doneByManager is done_by for an item the manager concluded needs nothing.
const doneByManager = "manager"

// noteSelfReplied records that the user answered in the source itself, not through Persona. Under b.mu.
func (b *inboxBook) noteSelfReplied(it *InboxItem, at time.Time) {
	b.chainAdd(it, InboxChainEntry{Kind: ChainSelfReplied, At: at.UTC(),
		Text: i18n.T(b.userLang(), "assistant-inbox_chain.self_replied", i18n.Args{"source": b.sourceName(it.Source)}), Link: &InboxChainLink{Type: LinkSource}})
}

// noteSent records a message Persona sent for the item. Under b.mu.
func (b *inboxBook) noteSent(it *InboxItem, at time.Time) {
	key := "sent_other"
	switch {
	case it.Lark != nil:
		key = "sent_lark"
	case it.Mail != nil:
		key = "sent_mail"
	case it.Doc != nil, it.Moment != nil, it.Connector != nil:
		key = "sent_comment"
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainSent, At: at.UTC(), Text: i18n.T(b.userLang(), "assistant-inbox_chain."+key), Link: sentLink(it)})
}

// sentLink is where a sent message is: Feishu's at the end of the chat (where a reply lands), else the source.
func sentLink(it *InboxItem) *InboxChainLink {
	if it.Mail != nil && len(it.Mail.UIDs) > 0 {
		return &InboxChainLink{Type: LinkSource, UID: it.Mail.UIDs[len(it.Mail.UIDs)-1]}
	}
	if it.Lark != nil && it.Last != "" {
		return &InboxChainLink{Type: LinkSource, Position: it.Last}
	}
	return sourceLink(it)
}

// noteHandling records that a processing conversation took the item. Under b.mu.
func (b *inboxBook) noteHandling(it *InboxItem, machineID, sessionID string) {
	for _, e := range it.Chain { // one entry per conversation
		if e.Kind == ChainHandling && e.Link != nil && e.Link.SessionID == sessionID {
			return
		}
	}
	b.chainAdd(it, InboxChainEntry{Kind: ChainHandling, Text: i18n.T(b.userLang(), "assistant-inbox_chain.handling"),
		Link: &InboxChainLink{Type: LinkSession, MachineID: machineID, SessionID: sessionID}})
}

// atomOf is the item's atom: the source's stable key of the thing (a chat, a mail thread, a PR), with its source.
func atomOf(it *InboxItem) string {
	switch {
	case it.Group != nil:
		return it.Group.Connector + "\x00" + it.Group.Thread
	case it.Connector != nil:
		return it.Source + "\x00" + it.Connector.Thread
	case it.Mail != nil:
		return "mail\x00" + it.Mail.AccountID + "\x00" + it.Mail.Thread
	}
	return it.Source + "\x00" + migratedThread(it)
}

// relate records that a and c are related across atoms (each lists the other); same-atom pairs are not. Under b.mu.
func relate(a, c *InboxItem) bool {
	if a == nil || c == nil || a == c || atomOf(a) == atomOf(c) {
		return false
	}
	add := func(x, y *InboxItem) bool {
		for _, r := range x.Related {
			if r.ID == y.ID {
				return false
			}
		}
		if len(x.Related) >= 10 {
			return false
		}
		x.Related = append(x.Related, InboxRelated{ID: y.ID, Title: y.Title, Source: y.Source})
		return true
	}
	one, two := add(a, c), add(c, a)
	return one || two
}

// status is the section of an open item ("" for a done one): a suggestion to act on, a failure, or something urgent
// the manager is still preparing is "Needs you"; the manager or its agent working, or a confirmed action waiting for the
// other side, is "In progress"; else the tier's.
func (it *InboxItem) status() string {
	if !it.open() {
		return ""
	}
	if it.Tier == TierNoise {
		return StatusNoise
	}
	if s := it.Suggestion; s != nil && s.Kind != SuggestConclusion {
		return StatusAction
	}
	if h := it.Handling; h != nil && it.Suggestion == nil {
		switch h.State {
		case HandlingLooking, HandlingAgent:
			if it.Urgent {
				return StatusAction
			}
			return StatusWorking
		case HandlingWaiting:
			return StatusWorking
		case HandlingFailed:
			return StatusAction
		}
	}
	if it.Tier == TierFYI {
		return StatusFYI
	}
	return StatusAction
}

// handled: a processing conversation works on the event or waits on what it did (looking, agent, waiting), or a
// suggestion of its waits for the user: what comes into it goes to that conversation, not to triage again.
func (it *InboxItem) handled() bool {
	if it.Suggestion != nil {
		return true
	}
	if h := it.Handling; h != nil {
		switch h.State {
		case HandlingLooking, HandlingAgent, HandlingWaiting:
			return true
		}
	}
	return false
}
