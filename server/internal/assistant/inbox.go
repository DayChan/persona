package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The unified inbox (docs/assistant/inbox.md) is the manager's: Feishu messages, mail and the connectors' threads in one list,
// one row per "thing" — the new messages in a chat, a mail thread — not per message and not per chat. The hub reads
// the sources when a round is due and hands what it read over (InboxCollected); the inbox keeps the items in Postgres
// (table inbox_items, only it reads and writes it) with their AI triage (inbox_triage.go) and whether the user has
// dealt with them, answers the /api/v1/inbox… routes the hub relays, and tells the apps {"type":"inbox"} (through the
// hub, assistantwire.EventInboxChanged) when the list changes.

const (
	inboxWindow   = 7 * 24 * time.Hour // older unread chats / mail are history, not inbox
	inboxKeepDone = 3 * 24 * time.Hour // done items stay listed (for undo) this long
	inboxForget   = 8 * 24 * time.Hour // and are remembered (so old unread messages don't come back) this long
	inboxText     = 600                // characters of one message kept for triage
)

// Item kinds.
const (
	KindDM      = "dm"      // a person wrote to me
	KindMention = "mention" // @me / @all in a group
	KindGroup   = "group"   // new messages in a group I follow (not muted)
	KindBot     = "bot"     // a bot or app chat
	KindMail    = "mail"
)

// Triage tiers (inbox_triage.go).
const (
	TierAction = "action" // "Needs you"
	TierFYI    = "fyi"    // "FYI"
	TierNoise  = "noise"  // "Noise"
)

type InboxItem struct {
	ID          string     `json:"id"`
	Source      string     `json:"source"` // lark | mail
	Kind        string     `json:"kind"`
	Account     string     `json:"account"`      // "feishu" or a mail account id
	AccountName string     `json:"account_name"` // Lark (Feishu) / the mail address
	Title       string     `json:"title"`        // chat name / mail subject
	Sender      string     `json:"sender,omitempty"`
	Avatar      string     `json:"avatar,omitempty"`
	Preview     string     `json:"preview"`
	Count       int        `json:"count"`
	StartedAt   time.Time  `json:"started_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
	Tier        string     `json:"tier,omitempty"` // empty until triaged
	Summary     string     `json:"summary,omitempty"`
	Reason      string     `json:"reason,omitempty"`
	Read        bool       `json:"read,omitempty"` // read somewhere else (Feishu, another mail client)
	DoneAt      *time.Time `json:"done_at,omitempty"`
	DoneBy      string     `json:"done_by,omitempty"` // user | dismissed | read | replied | superseded
	Lark        *InboxLark `json:"lark,omitempty"`
	// Conversation is where the item opens in a messaging source (docs/extensions/extension-messaging.md "9", #2138): listed
	// only, from Lark; an app with the source opens it there, an older one uses Lark.
	Conversation *wire.MessagingConversationRef `json:"conversation,omitempty"`
	Mail         *InboxMail                     `json:"mail,omitempty"`
	Doc          *InboxDoc                      `json:"doc,omitempty"` // a Feishu document's @me / comment (inbox_blocks.go)
	// Translations by target language: new foreign mail, translated before it is opened (inbox_translate.go).
	Translations map[string]*InboxTranslation `json:"translations,omitempty"`
	Moment       *InboxMoment                 `json:"moment,omitempty"` // a Moments (company feed) post's comments / @me (inbox_blocks.go)
	// Connector: a thread of a connector's (GitHub…), Source being its id (inbox_connectors.go).
	Connector *InboxConnector `json:"connector,omitempty"`
	// Agent is the AI twin's work on the item: judged by model 2, handled and drafted by the agent (inbox_agent.go).
	Agent *InboxAgent `json:"agent,omitempty"`
	// Mutes are what it can be muted by (listed only; inbox_mutes.go).
	Mutes []MuteOption `json:"mutes,omitempty"`
	// Version names what the item holds now (listed only): a bulk "Done" sends it back, so an item that took new
	// messages since the list was drawn is not filed with them (inbox_bulk.go).
	Version string `json:"version,omitempty"`
	// TriagedBy says who sorted an event (inbox_group.go): "model" (its title, summary and tier are the model's) or
	// "rules" (the connector grouped it and its rule tier stands, "By rule").
	TriagedBy string `json:"triaged_by,omitempty"`
	// Parent is what the item grew out of: another item, a todo or an agent session (inbox_parent.go, #2128).
	Parent *InboxParent `json:"parent,omitempty"`

	// The event's part (inbox_events.go, inbox_handling.go): its section (listed only), urgent (someone waits, a
	// deadline is near), the manager's recap, the processing conversation, the one current suggestion, the handling chain
	// and the items related to it across atoms.
	Status     string            `json:"status,omitempty"`
	Urgent     bool              `json:"urgent,omitempty"`
	Recap      string            `json:"recap,omitempty"`
	Handling   *InboxHandling    `json:"handling,omitempty"`
	Suggestion *InboxSuggestion  `json:"suggestion,omitempty"`
	Chain      []InboxChainEntry `json:"chain,omitempty"`
	Related    []InboxRelated    `json:"related,omitempty"`
	// Handoffs are the latest conversations the event was handed to (inbox_handoff.go), oldest first.
	Handoffs []InboxHandoff `json:"handoffs,omitempty"`

	// Bookkeeping: persisted, not listed (GET /api/v1/inbox/{id} returns Context).
	Last      string `json:"last,omitempty"`       // newest message position / mail uid in the item
	Context   string `json:"context,omitempty"`    // the conversation as text, for triage and "Hand to AI"
	TriagedAt string `json:"triaged_at,omitempty"` // Last when it was triaged
	BodyOf    string `json:"body_of,omitempty"`    // mail: Last when the full text was read into Preview / Context
	JudgedAt  string `json:"judged_at,omitempty"`  // Last when model 2 looked at it
	// Rules are the item's own triage rules, from its connector (InboxThing.Rules): they replace inbox.rules for it.
	Rules string `json:"rules,omitempty"`
	// ArrivedAt is the clock here when the item first held Last (ArrivedOf), as listed or saved: a bulk "Done" over a
	// filter leaves what arrived after the list it acted on (inbox_bulk.go). Rows stored before have UpdatedAt.
	ArrivedOf string    `json:"arrived_of,omitempty"`
	ArrivedAt time.Time `json:"arrived_at,omitzero"`
	// Meta is an event's meta as its connector wrote it (which messages are in it…): kept and handed back as it is,
	// never read here. Group is the event's conversation (inbox_group.go).
	Meta  json.RawMessage `json:"meta,omitempty"`
	Group *InboxGroup     `json:"group,omitempty"`
	// ChainSeq numbers the chain's entries; SuggestSeq is the last suggestion's version (a new one is the next).
	ChainSeq   int `json:"chain_seq,omitempty"`
	SuggestSeq int `json:"suggest_seq,omitempty"`
}

type InboxLark struct {
	AccountID int `json:"account_id,omitempty"` // zero in legacy rows means primary account 1
	// OLAccount is what the apps pass as ol_account to the /api/v1/lark routes about this item: AccountID when that
	// is not lark.account, else zero (larkRequestAccount).
	OLAccount       int    `json:"ol_account,omitempty"`
	FeedID          string `json:"feed_id"`
	IsGroup         bool   `json:"is_group"`
	IsTopic         bool   `json:"is_topic,omitempty"`
	StartPosition   string `json:"start_position,omitempty"`   // the first new message (open the chat there)
	ReadPosition    string `json:"read_position,omitempty"`    // the chat's read position in Feishu, as of the last collection
	MentionPosition string `json:"mention_position,omitempty"` // the unread @me
	AtAll           bool   `json:"at_all,omitempty"`
	AtMe            bool   `json:"at_me,omitempty"` // an unread @me (not only @all): kept while the item is open
	// ReplyTo is the newest message from someone else; ThreadKind says where it sits: "" in the chat, "topic" in a
	// topic group's topic, "thread" in a thread of a normal group. A reply from the inbox goes there (the plugin's).
	ReplyTo    string `json:"reply_to,omitempty"`
	ThreadKind string `json:"thread_kind,omitempty"`
	// SenderID is who sent the newest message from someone else (a group's sender, to mute them).
	SenderID string `json:"sender_id,omitempty"`
}

// Values of InboxLark.ThreadKind.
const (
	LarkThreadTopic  = "topic"
	LarkThreadThread = "thread"
)

// MailAddress is a mail's sender or recipient.
type MailAddress = assistantwire.MailAddress

type InboxMail struct {
	AccountID string      `json:"account_id"`
	Mailbox   string      `json:"mailbox"`
	UIDs      []uint32    `json:"uids"` // oldest first
	MessageID string      `json:"message_id"`
	From      MailAddress `json:"from"`
	Direct    bool        `json:"direct,omitempty"` // to me, not cc / a list
	Thread    string      `json:"thread"`           // normalized subject: replies join the same item
}

func (it *InboxItem) open() bool { return it.DoneAt == nil }

// touched stamps a new per-id parent/done mutation generation under b.mu: a rollback checks its stamp to tell its
// own write apart from a concurrent same-valued one (two reopens, two unlinks), which value equality cannot.
func (b *inboxBook) touched(id string) uint64 {
	if b.mark == nil {
		b.mark = map[string]uint64{}
	}
	b.mark[id]++
	return b.mark[id]
}

// conversationOf is where a chat item opens in its messaging source: the source is the connector's id ("lark", the
// Lark extension's source of the same id), the account the one the apps would pass as ol_account.
func conversationOf(it *InboxItem) *wire.MessagingConversationRef {
	if it.Lark == nil || it.Lark.FeedID == "" || it.Source == "" {
		return nil
	}
	ref := &wire.MessagingConversationRef{Source: it.Source, ID: it.Lark.FeedID, StartPosition: it.Lark.StartPosition,
		ReadPosition: it.Lark.ReadPosition, MentionPosition: it.Lark.MentionPosition}
	if it.Lark.OLAccount > 0 {
		ref.Account = strconv.Itoa(it.Lark.OLAccount)
	}
	return ref
}

// listed is the item as the list shows it (no context).
func (it *InboxItem) listed() InboxItem {
	c := *it
	c.Context, c.Last, c.TriagedAt, c.BodyOf, c.JudgedAt, c.Rules, c.ArrivedOf = "", "", "", "", "", "", ""
	c.ArrivedAt = time.Time{}
	c.Meta, c.Group = nil, nil
	c.ChainSeq, c.SuggestSeq = 0, 0
	c.Status = it.status()
	c.Chain = listedChain(it)
	c.Related = slices.Clone(it.Related)
	c.Handoffs = listedHandoffs(it.Handoffs)
	if it.Handling != nil {
		h := *it.Handling
		c.Handling = &h
	}
	if g := currentSuggestion(it); g != nil {
		g := *g
		g.Changes, g.Cwd = slices.Clone(g.Changes), ""
		c.Suggestion = &g
		if c.Handling == nil && g.Conversation == nil && it.Agent != nil { // an old draft: its conversation is done with this round
			c.Handling = &InboxHandling{Owner: HandlingOwner, State: HandlingDone, MachineID: it.Agent.MachineID, SessionID: it.Agent.SessionID}
		}
	}
	c.Mutes = mutesOf(it)
	c.Version = inboxVersion(it)
	c.Conversation = conversationOf(it)
	if it.Agent != nil { // the copy is encoded after the lock is released
		a := *it.Agent
		a.Pending, a.Wake, a.SentOf, a.Lost = nil, false, "", nil
		c.Agent = &a
	}
	if it.Mail != nil {
		m := *it.Mail
		m.UIDs = append([]uint32(nil), m.UIDs...)
		c.Mail = &m
	}
	if it.Doc != nil {
		d := *it.Doc
		c.Doc = &d
	}
	if len(it.Translations) > 0 { // the list shows subject and preview; the body is fetched when the mail opens
		c.Translations = map[string]*InboxTranslation{}
		for k, t := range it.Translations {
			if t != nil && t.Of == it.Last {
				c.Translations[k] = &InboxTranslation{Subject: t.Subject, Preview: t.Preview, Clipped: t.Clipped}
			}
		}
	}
	if it.Moment != nil {
		m := *it.Moment
		m.Notices = append([]string(nil), it.Moment.Notices...)
		c.Moment = &m
	}
	if it.Connector != nil {
		k := *it.Connector
		c.Connector = &k
	}
	return c
}

// Listed is the item as GET /api/v1/inbox lists it (the hub's wire fixtures are made from it).
func (it InboxItem) Listed() InboxItem { return it.listed() }

// inboxBook is the inbox's state.
type inboxBook struct {
	lang        func() i18n.Lang // the user's language, for the reasons it writes on items (nil: the default)
	larkAccount int              // lark.account (InboxConfig.LarkAccount); zero is primary account 1
	mu          sync.Mutex
	items       map[string]*InboxItem
	once        sync.Once
	loadMu      sync.Mutex              // one load at a time
	loaded      atomic.Bool             // what the database keeps was read: until then nothing is taken or written
	running     sync.Mutex              // one collection applied at a time
	saving      sync.Mutex              // snapshots and writes in the same order
	mark        map[string]uint64       // bumped on each user parent/done mutation of an id, under mu: a rollback's identity check
	filing      map[string]*inboxFiling // pending filing ownership, independent of an unrelated parent change
	epoch       string                  // the hub's run and its last round applied (InboxRound.Epoch, Seq), under running
	seq         uint64
	triaging    sync.Mutex  // one triage pass at a time (inbox_triage.go)
	handing     sync.Mutex  // one judge / agent pass at a time (inbox_agent.go)
	handAgain   atomic.Bool // a pass is asked for: the goroutine holding handing runs it (handInbox)
	passHook    func()      // tests: runs as each pass starts
	translating sync.Mutex  // one translation pass at a time (inbox_translate.go)
	replies     sync.Map    // item id -> *inboxReply: replies to one item, one at a time (inbox_triage.go)
	handingOff  sync.Map    // item id -> *sync.Mutex: hand-offs of one item, one at a time (inbox_handoff.go)
	telling     sync.Map    // item id -> *sync.Mutex: deliveries to one item's conversation, one at a time (deliverNotes)
	mutes       []InboxMute
	mutesMu     sync.Mutex // one read-change-save of the kept mutes at a time
	policies    []InboxPolicy
	policiesMu  sync.Mutex // one read-change-save of the kept policies at a time (inbox_policies.go)
	cardSends   cardLocks  // a message card's id → the lock its "Send" holds (sendFromCard)
	// translations are the translations made, by model, text or mail and language (inbox_translate.go).
	translations translateCache

	// What the rounds and the hub's settings said of the sources (under srcMu, which is taken after mu when both
	// are): the plugins' connectors (their new items are triaged before they enter; InboxConfig.Extensions names them
	// from the start), the built-in ones a round read, how they describe their kinds, every connector's name.
	srcMu      sync.RWMutex
	extensions map[string]bool
	builtin    map[string]bool
	kinds      map[string]string // "<connector>/<kind>" → words
	names      map[string]string

	// admitting is one triage of a round's new plugin items at a time (admitInbox): a round meanwhile does not start
	// them again.
	admitting sync.Mutex
	batch     *inboxBatch          // while a collection runs: the items it starts, which enter once triaged
	held      map[string]inboxHeld // by source: the new items its last collection held back (inbox_admit.go)
	// readErrs: the sources the last round could not read, by id, as a short code (assistantwire.ReadFailed…): the
	// apps say which one is behind (InboxTriage.Sources). Memory only.
	readErrs map[string]string
	grouping inboxGrouping // the batches being related to events (inbox_group.go)

	cfgMu sync.Mutex
	cfg   assistantwire.InboxConfig // the hub's (InboxConfigMethod), as last read
	cfgAt time.Time
}

func (b *inboxBook) init() {
	b.once.Do(func() {
		b.items = map[string]*InboxItem{}
		b.extensions, b.builtin, b.kinds, b.names = map[string]bool{}, map[string]bool{}, map[string]string{}, map[string]string{}
	})
}

// fromExtension says whether a connector (an item's source) is a plugin's, as the rounds said: its new items are
// triaged before they enter (inbox_admit.go).
func (b *inboxBook) fromExtension(source string) bool {
	b.srcMu.RLock()
	defer b.srcMu.RUnlock()
	return b.extensions[source]
}

// builtinSource says whether a connector is one of the hub's own, as a round said. A connector's item from a source
// neither a round nor the settings named (the manager just started) has no fallback tier: never noise by default.
func (b *inboxBook) builtinSource(source string) bool {
	b.srcMu.RLock()
	defer b.srcMu.RUnlock()
	return b.builtin[source] && !b.extensions[source]
}

// knowExtensions takes the plugins' connectors the hub's settings name (InboxConfig.Extensions).
func (b *inboxBook) knowExtensions(ids []string) {
	b.srcMu.Lock()
	defer b.srcMu.Unlock()
	for _, id := range ids {
		b.extensions[id] = true
	}
}

// kindWords is how a plugin's connector describes one of its kinds of things ("" when it does not).
func (b *inboxBook) kindWords(source, kind string) string {
	b.srcMu.RLock()
	defer b.srcMu.RUnlock()
	return b.kinds[source+"/"+kind]
}

// userLang is the user's language for what b writes for people.
func (b *inboxBook) userLang() i18n.Lang {
	if b.lang == nil {
		return i18n.Default
	}
	return b.lang()
}

// byRules is an item's reason when its rules, not the model, filed it.
func (b *inboxBook) byRules() string { return i18n.T(b.userLang(), "assistant-inbox.by_rules") }

// mutedReason is an item's reason when mute name closed it.
func (b *inboxBook) mutedReason(name string) string {
	return i18n.T(b.userLang(), "assistant-inbox.muted", i18n.Args{"name": name})
}

// sourceName is an inbox source for people: Lark (Feishu), mail, a connector's name as the hub last said it.
func (b *inboxBook) sourceName(source string) string {
	switch source {
	case "lark":
		return i18n.T(b.userLang(), "assistant-inbox.source_lark")
	case "mail":
		return i18n.T(b.userLang(), "assistant-inbox.source_mail")
	}
	b.srcMu.RLock()
	defer b.srcMu.RUnlock()
	if n := b.names[source]; n != "" {
		return n
	}
	return source
}

// drop forgets an item, under b.mu, with its reply and delivery locks: they would otherwise stay for the life of the
// hub. A delivery holding or waiting for the lock finds the item gone and stops (deliverNotes).
func (b *inboxBook) drop(id string) {
	delete(b.items, id)
	delete(b.filing, id)
	b.replies.Delete(id)
	b.telling.Delete(id)
	b.handingOff.Delete(id)
}

// ---- helpers ----

// threadIndex is the items (and the running round's) by thread, for a collection that looks up one thread per
// thing: openFor on each scanned the whole inbox, a week of chats for each of Feishu's few hundred unread ones.
// key is the item's thread among the ones looked up, false for an item of another source. Under b.mu.
type threadIndex struct {
	key func(*InboxItem) (string, bool)
	by  map[string][]*InboxItem
}

func (b *inboxBook) threads(key func(*InboxItem) (string, bool)) *threadIndex {
	x := &threadIndex{key: key, by: map[string][]*InboxItem{}}
	b.each(x.note)
	return x
}

// note files it under its thread: an item added, or one whose thread may have changed. An entry under a thread it
// has left is skipped by the lookups.
func (x *threadIndex) note(it *InboxItem) {
	if k, ok := x.key(it); ok && !slices.Contains(x.by[k], it) {
		x.by[k] = append(x.by[k], it)
	}
}

// openFor finds the open item of a thread (one this round started too); done is the newest done one. A thread can
// have two open items (an older one moved back after a newer one started): the newest gathers what comes next, the
// same one every collection, so the older one does not become a copy of it (#3796).
func (x *threadIndex) openFor(thread string) (open, done *InboxItem) {
	for _, it := range x.by[thread] {
		if k, ok := x.key(it); !ok || k != thread {
			continue
		}
		if it.open() {
			if open == nil || newerItem(it, open) {
				open = it
			}
		} else if done == nil || it.UpdatedAt.After(done.UpdatedAt) {
			done = it
		}
	}
	return open, done
}

// newerItem: a holds a later move than b (by its time, then its id, so a tie picks the same one every time).
func newerItem(a, b *InboxItem) bool {
	if !a.UpdatedAt.Equal(b.UpdatedAt) {
		return a.UpdatedAt.After(b.UpdatedAt)
	}
	return a.ID > b.ID
}

// each calls f for every item, and every item the running round started, under b.mu.
func (b *inboxBook) each(f func(*InboxItem)) {
	for _, it := range b.items {
		f(it)
	}
	if b.batch != nil {
		for _, it := range b.batch.items {
			f(it)
		}
	}
}

// has says whether an item (or one the running round started) has id, under b.mu.
func (b *inboxBook) has(id string) bool {
	_, ok := b.items[id]
	return ok || b.batch != nil && b.batch.items[id] != nil
}

// add keeps an item a collection starts (from: the source it came from), under b.mu. A plugin connector's, while a
// round runs, goes to the round's batch and enters the list once triaged (admitInbox); any other straight in.
func (b *inboxBook) add(from string, it *InboxItem) {
	if b.batch == nil || !b.fromExtension(from) {
		b.items[it.ID] = it
		return
	}
	b.batch.items[it.ID] = it
	b.batch.from[it.ID] = from
}

// readSource notes that the running round read a source: its held-back count is recomputed after the round.
func (b *inboxBook) readSource(id, name string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.batch != nil {
		b.batch.read[id] = name
	}
}

// arrived stamps when the item came to hold what it holds (ArrivedAt), the first time it is listed or saved so. Under
// b.mu.
func (it *InboxItem) arrived(now time.Time) {
	if it.ArrivedOf != it.Last || it.ArrivedAt.IsZero() {
		it.ArrivedOf, it.ArrivedAt = it.Last, now
	}
}

// arrivedAfter: the item took what it holds after asOf (or has not been stamped yet: it is coming in now). Under b.mu.
func (it *InboxItem) arrivedAfter(asOf time.Time) bool {
	return it.ArrivedOf != it.Last || it.ArrivedAt.After(asOf)
}

func (it *InboxItem) done(at time.Time, by string) {
	t := at
	it.DoneAt, it.DoneBy = &t, by
}

// ---- API ----

var errNoInboxItem = i18n.ErrEnglish("inbox-errors.no_item")

// errInboxItemDone is "Send" on a message card whose inbox item was filed since (sendFromCard): nothing is sent.
var errInboxItemDone = i18n.ErrEnglish("inbox-errors.item_done")

// errInboxNotPersisted says the item moved in memory but its row could not be written; the in-memory move was
// rolled back, so the request can be retried.
var errInboxNotPersisted = i18n.ErrEnglish("inbox-errors.save_failed")

var errCannotReply = i18n.ErrEnglish("inbox-errors.cannot_reply")

func (s *Service) inboxRoutes() {
	s.inboxMuteRoutes()
	s.inboxPolicyRoutes()
	s.inboxParentRoutes()
	s.inboxBulkRoutes()
	s.inboxSuggestionRoutes()
	s.inboxHandoffRoutes()
	s.translateRoutes()
	// Items of connectors (GitHub…) only go to apps that draw them (?sources=all): an older app would show a PR as
	// a mail and send a reply as one. triage says how many new items wait for the model, and why (inbox_admit.go).
	s.handle("GET /api/v1/inbox", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		items, asOf := s.inboxListAt(r.URL.Query().Get("sources") == "all")
		apierr.WriteJSON(w, map[string]any{"items": items, "as_of": asOf, "triage": s.inboxTriage()})
	})
	s.handle("GET /api/v1/inbox/{id}", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		b := &s.inbox
		b.mu.Lock()
		it, ok := b.items[r.PathValue("id")]
		var c InboxItem
		if ok {
			c = it.listed()
			c.Context = it.Context
		}
		b.mu.Unlock()
		if !ok {
			apierr.Write(w, http.StatusNotFound, errNoInboxItem)
			return
		}
		apierr.WriteJSON(w, struct {
			InboxItem
			ReplyTarget *inboxMailTarget `json:"reply_target,omitempty"`
		}{c, mailTarget(c)})
	})
	s.handle("POST /api/v1/inbox/{id}/done", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Done    bool `json:"done"`
			Dismiss bool `json:"dismiss"` // "Ignore": filed as not for the user, done_by=dismissed
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
		it, err := s.setInboxDone(r.Context(), r.PathValue("id"), in.Done, in.Dismiss)
		if err != nil {
			if errors.Is(err, errNoInboxItem) {
				apierr.Write(w, http.StatusNotFound, err)
			} else {
				apierr.Write(w, http.StatusInternalServerError, err)
			}
			return
		}
		apierr.WriteJSON(w, it)
	})
	s.handle("POST /api/v1/inbox/{id}/draft", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Instruction string `json:"instruction"`
		}
		_ = json.NewDecoder(r.Body).Decode(&in)
		text, err := s.draftReply(r.Context(), r.PathValue("id"), in.Instruction)
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		default:
			apierr.WriteJSON(w, map[string]any{"text": text})
		}
	})
	s.handle("POST /api/v1/inbox/{id}/reply", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in struct {
			Text            string      `json:"text"`
			Changes         []DocChange `json:"changes"` // documents: status moves to make with the reply
			ClientID        string      `json:"client_id"`
			ExpectedUID     *uint32     `json:"expected_uid"`
			ExpectedVersion *string     `json:"expected_version"`
			Card            string      `json:"card"` // the message card it is sent from (sendFromCard)
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || strings.TrimSpace(in.Text) == "" && len(in.Changes) == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.text_required"))
			return
		}
		if !fullRole(r) {
			// Posted as the user, a "/agent …" comes back as their request and starts an agent (docs/extensions/lark-agent.md).
			if len(in.Changes) > 0 || assistantwire.AsksAgent(in.Text) {
				apierr.Write(w, http.StatusForbidden, i18n.ErrEnglish("inbox-errors.reply_text_only"))
				return
			}
		}
		send := func() (InboxItem, error) {
			return s.replyInboxChecked(r.Context(), r.PathValue("id"), strings.TrimSpace(in.Text), in.ClientID, in.Card != "", in.ExpectedUID, in.ExpectedVersion, in.Changes...)
		}
		var it InboxItem
		var err error
		if in.Card != "" {
			it, _, err = s.sendFromCard(r.Context(), in.Card, r.PathValue("id"), send)
		} else {
			it, err = send()
		}
		switch {
		case errors.Is(err, errMailReplyTargetChanged):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "mail_reply_target_changed", "error": i18n.T(i18n.OfWriter(w), "assistant-inbox.mail_target_changed"), "retryable": false, "delivered": "none"})
		case errors.Is(err, errInboxItemDone):
			apierr.WriteBody(w, http.StatusConflict, map[string]any{"code": "inbox_item_done", "error": i18n.T(i18n.OfWriter(w), "assistant-inbox.item_done"), "retryable": false, "delivered": "none"})
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case replyInDoubt(err):
			// It may have gone out: the apps say so (delivered unknown); the same reply again is not sent twice.
			body := apierr.LocalizedBody(http.StatusBadGateway, assistantwire.CodeAssistantInDoubt, err, i18n.OfWriter(w))
			body["delivered"], body["retryable"] = "unknown", false
			apierr.WriteBody(w, http.StatusBadGateway, body)
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		default:
			apierr.WriteJSON(w, it)
		}
	})
	s.handle("POST /api/v1/inbox/{id}/doc/status", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		var in DocChange
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.Ref == "" || in.Field == "" || in.Value == "" {
			apierr.Write(w, http.StatusBadRequest, i18n.ErrEnglish("inbox-errors.doc_change_required"))
			return
		}
		it, err := s.setDocStatus(r.Context(), r.PathValue("id"), in)
		switch {
		case errors.Is(err, errNoInboxItem):
			apierr.Write(w, http.StatusNotFound, err)
		case errors.Is(err, errNoDocRow):
			apierr.Write(w, http.StatusBadRequest, err)
		case err != nil:
			apierr.Write(w, http.StatusBadGateway, err)
		default:
			apierr.WriteJSON(w, it)
		}
	})
	// A pull to refresh: the hub reads every source now and hands it over before it answers.
	s.handle("POST /api/v1/inbox/refresh", func(w http.ResponseWriter, r *http.Request) {
		if !s.inboxUp(w, r) {
			return
		}
		if err := s.appCall(r.Context(), AppInbox, assistantwire.InboxCollect, map[string]any{"round": assistantwire.RoundFull}, nil); err != nil {
			s.logf("inbox: refresh: %v", err)
		}
		items, asOf := s.inboxListAt(r.URL.Query().Get("sources") == "all")
		apierr.WriteJSON(w, map[string]any{"items": items, "as_of": asOf, "triage": s.inboxTriage()})
	})
}

type inboxMailTarget struct {
	UID     uint32 `json:"uid"`
	Version string `json:"version"`
}

var errMailReplyTargetChanged = i18n.ErrEnglish("inbox-errors.mail_target_changed")

func mailTarget(it InboxItem) *inboxMailTarget {
	if it.Mail == nil || len(it.Mail.UIDs) == 0 {
		return nil
	}
	// Bind the version to the account, mailbox, message and recipient, not read/triage state.
	raw, _ := json.Marshal(it.Mail)
	sum := sha256.Sum256(raw)
	return &inboxMailTarget{UID: it.Mail.UIDs[len(it.Mail.UIDs)-1], Version: hex.EncodeToString(sum[:])}
}

// replyInboxChecked is replyInboxFrom with the mail the app saw (expected_uid / expected_version), if it gave one.
func (s *Service) replyInboxChecked(ctx context.Context, id, text, clientID string, fromCard bool, uid *uint32, version *string, changes ...DocChange) (InboxItem, error) {
	if uid == nil && version == nil {
		return s.replyInboxFrom(ctx, id, text, clientID, fromCard, changes...)
	}
	// Collection and checked sends must not change the target between validation and sending.
	b := &s.inbox
	b.running.Lock()
	defer b.running.Unlock()
	b.mu.Lock()
	it := b.items[id]
	var target *inboxMailTarget
	if it != nil {
		target = mailTarget(*it)
	}
	b.mu.Unlock()
	if it == nil {
		return InboxItem{}, errNoInboxItem
	}
	if target == nil || uid != nil && *uid != target.UID || version != nil && *version != target.Version {
		return InboxItem{}, errMailReplyTargetChanged
	}
	return s.replyInboxFrom(ctx, id, text, clientID, fromCard, changes...)
}

// inboxList is every open item and the recently done ones, newest first; connectors' only when all (rows stored
// before Source was set have none, so it is the connector block that tells).
func (s *Service) inboxList(all bool) []InboxItem {
	out, _ := s.inboxListAt(all)
	return out
}

// inboxListAt is inboxList and the moment it was read (as_of): everything listed arrived by then.
func (s *Service) inboxListAt(all bool) ([]InboxItem, time.Time) {
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	cutoff := now.Add(-inboxKeepDone)
	out := make([]InboxItem, 0, len(b.items))
	for _, it := range b.items {
		if it.DoneAt != nil && it.DoneAt.Before(cutoff) || !all && it.Connector != nil {
			continue
		}
		it.arrived(now)
		out = append(out, it.listed())
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UpdatedAt.After(out[j].UpdatedAt) })
	return out, now
}

func (s *Service) setInboxDone(ctx context.Context, id string, done, dismiss bool) (InboxItem, error) {
	b := &s.inbox
	b.mu.Lock()
	it, ok := b.items[id]
	if !ok {
		b.mu.Unlock()
		return InboxItem{}, errNoInboxItem
	}
	w := b.beginFiling(it, time.Now(), doneByOf(dismiss))
	if done {
		if it.open() {
			b.file(it, w.now, w.why)
			w.close = true
		} else {
			// Another filing may still be waiting on storage. Taking over its marker also takes over the closure;
			// an item already durably filed keeps its handling as before.
			if w.previousWrite != nil && w.previousWrite.close {
				// This is another attempt while the earlier filing is uncommitted, with its own chain entry.
				b.file(it, w.now, w.why)
				w.close = true
			} else {
				it.done(w.now, w.why)
			}
		}
	} else {
		it.DoneAt, it.DoneBy = nil, ""
	}
	b.settleFiling(w)
	b.mu.Unlock()
	failed := s.saveInboxFilings(ctx, []*inboxFiling{w})
	if failed[id] {
		return InboxItem{}, errInboxNotPersisted
	}
	b.mu.Lock()
	out := it.listed()
	b.mu.Unlock()
	return out, nil
}

// ---- what the hub asks of the inbox ----

// InboxItemJSON is inbox.item: one item as it is stored (an extension's inbox_item slot draws it).
func (s *Service) InboxItemJSON(ctx context.Context, id string) (json.RawMessage, bool) {
	if s.inboxReady(ctx) != nil {
		return nil, false
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	it := b.items[id]
	if it == nil {
		return nil, false
	}
	raw, _ := json.Marshal(it)
	return raw, true
}

// InboxForget is inbox.forget: the user deleted a source's data; its items go too. It says how many went.
func (s *Service) InboxForget(ctx context.Context, source string) (int, error) {
	if err := s.inboxReady(ctx); err != nil {
		return 0, err
	}
	b := &s.inbox
	b.saving.Lock()
	b.mu.Lock()
	var ids []string
	for id, it := range b.items {
		if it.Source == source {
			ids = append(ids, id)
		}
	}
	b.mu.Unlock()
	if len(ids) == 0 {
		b.saving.Unlock()
		return 0, nil
	}
	// Persist the deletion first: if it fails the items stay in memory, so a retry collects the same ids and
	// finishes the job (the connector delete contract relies on a retry cleaning up what is left).
	if st := s.inboxDB(); st != nil {
		if err := st.DeleteInboxItems(ctx, ids); err != nil {
			b.saving.Unlock()
			return 0, err
		}
	}
	b.mu.Lock()
	for _, id := range ids {
		b.drop(id)
	}
	b.mu.Unlock()
	b.saving.Unlock()
	s.inboxChanged(ctx, nil)
	return len(ids), nil
}

// inboxReport is what the hub shows of the inbox (the snapshot's inbox): the AI twin's conversations; nil while the
// inbox could not be read.
func (s *Service) inboxReport(ctx context.Context) *assistantwire.InboxReport {
	if s.inboxReady(ctx) != nil {
		return nil
	}
	b := &s.inbox
	b.mu.Lock()
	defer b.mu.Unlock()
	out := &assistantwire.InboxReport{Agents: []SessionRef{}}
	for _, it := range b.items {
		if a := it.Agent; a != nil && a.SessionID != "" {
			out.Agents = append(out.Agents, SessionRef{MachineID: a.MachineID, SessionID: a.SessionID})
		}
	}
	sort.Slice(out.Agents, func(i, j int) bool { return out.Agents[i].SessionID < out.Agents[j].SessionID })
	return out
}
