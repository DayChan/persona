package assistant

import (
	"context"
	"github.com/nwverse/persona/server/internal/compat"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Assistant conversations are titled "MMDD | kind | topic" (docs/assistant/assistant.md "Chat titles", the user's ask on 2026-11-01):
// MMDD is the day the conversation started in the user's time zone, kind one of titleKinds, topic at most 14
// characters. A conversation the hub starts is noted (titleNew); when its first turn ends (titleAfterTurn) the
// small-task model reads that exchange and names it, off the conversation's path: nothing waits for it and the
// assistant isn't asked to do anything. A scheduled task's conversation is "MMDD | scheduled | task name" from the start,
// with no model call; it stays the same however many runs go into it.
//
// The titles live in the assistant's store (settings assistant.titles) and go to the hub in the snapshot, which puts
// them on the session lists the apps get, so Claude and Codex conversations are named the same way and nothing is
// written into transcripts. A conversation is named once: a rename in the app (POST …/rename with a title) is
// recorded as the user's and is never replaced; conversations from before this are left as they were. The main
// conversation has no title of its own (the hub shows it as "Persona", #1313). A naming that ended without an answer
// leaves "explore | the first message" standing in, and a later turn names it again (#1532).

var titleKinds = []string{"功能", "设计", "修复", "优化", "发布", "探索", "文档", "研究", "评审", "定时"}

const (
	maxTitleTopic = 14 // characters, in Chinese; titleTopicMax for the user's language
	titlesKey     = "assistant.titles"
	titleTask     = "定时"
	// titleInbox is the inbox's AI twin's kind (#3264): the hub's own, not one the model picks (titleKinds).
	titleInbox    = "收件箱"
	titleFallback = "探索"
)

// What one naming may spend: each model call and the whole naming (reading the first exchange too); tests shorten
// them. The title model is often a thinking one behind a gateway: a title-sized call measured p50 6 s and p90 21.6 s
// there (#1532). Nothing waits for a title.
var (
	titleCallTimeout = 45 * time.Second
	titleBudget      = 2 * time.Minute
)

// titleMaxTokens is a title reply's room: a thinking model's thinking comes out of it (188-192 tokens of a 200 room
// left half a topic, #1532).
const titleMaxTokens = 1024

// maxTitleTries is how many namings a conversation gets when none of them ends with an answer (a gateway that is down,
// calls that keep timing out): the stand-in title stays after that.
const maxTitleTries = 3

// Who named a conversation.
const (
	titlePending = "pending" // started by the hub, named when its first turn ends
	titleAuto    = "auto"
	titleByTask  = "task"
	titleByInbox = "inbox" // the inbox's AI twin: "inbox | the item's title"
	titleByUser  = "user"
	// The assistant renamed it (conversation_rename): not replaced by the automatic title, the user's rename wins.
	titleByAssistant = "assistant"
)

type convTitle struct {
	MachineID string    `json:"machine_id"`
	Title     string    `json:"title,omitempty"`
	By        string    `json:"by"`
	Started   time.Time `json:"started"`
	// Tries counts the automatic namings that ended for want of an answer: Title is the stand-in, named again after a
	// later turn until maxTitleTries.
	Tries int `json:"tries,omitempty"`
}

// awaitsName: the hub names the conversation after its next turn: never named yet, or only given the stand-in.
func (t *convTitle) awaitsName() bool {
	return t.By == titlePending || t.By == titleAuto && t.Tries > 0 && t.Tries < maxTitleTries
}

type titleBook struct {
	mu     sync.Mutex
	loaded bool
	m      map[string]*convTitle // by session id
	naming map[string]bool       // a model call is out for these
}

// isMain: id is the main conversation (the assistant's machine is not checked: session ids are unique).
func (s *Service) isMain(ctx context.Context, id string) bool {
	return id != "" && s.settings(ctx).Main == id
}

// titlesLocked reads the book once; b.mu is held. A failed read leaves it unloaded (nothing is saved over it).
func (s *Service) titlesLocked(ctx context.Context) bool {
	b := &s.titles
	if b.loaded {
		return true
	}
	m := map[string]*convTitle{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, titlesKey, &m); err != nil {
			s.logf("load assistant titles: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]*convTitle{}
	}
	b.m, b.loaded = m, true
	return true
}

func (s *Service) saveTitlesLocked() {
	st := s.store()
	if st == nil {
		return
	}
	// A conversation that never finished its first turn in a day won't: forget it.
	for id, t := range s.titles.m {
		if t.By == titlePending && time.Since(t.Started) > 24*time.Hour {
			delete(s.titles.m, id)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := st.PutSetting(ctx, titlesKey, s.titles.m); err != nil {
		s.logf("save assistant titles: %v", err)
	}
}

// titleNew notes a conversation the hub just started, to name it after its first turn.
func (s *Service) titleNew(machine, id string, started time.Time) {
	s.noteTitle(machine, id, started, "", "")
}

// titleFixed names a conversation the hub just started for something that has a name already: "MMDD | kind |
// topic" now, with no model call (a scheduled task's "scheduled | task name", the inbox's "inbox | the item's title").
func (s *Service) titleFixed(machine, id string, started time.Time, kind, topic string) {
	s.noteTitle(machine, id, started, kind, topic)
}

func (s *Service) noteTitle(machine, id string, started time.Time, kind, topic string) {
	if id == "" {
		return
	}
	ctx := context.Background()
	t := &convTitle{MachineID: machine, By: titlePending, Started: started.UTC()}
	if kind != "" {
		if topic = titleTopic(topic); topic == "" {
			topic = "新对话"
		}
		t.Title, t.By = titleDay(started, s.location(ctx))+" | "+kind+" | "+topic, titleByTask
		if kind == titleInbox {
			t.By = titleByInbox
		}
	}
	b := &s.titles
	b.mu.Lock()
	if !s.titlesLocked(ctx) {
		b.mu.Unlock()
		return
	}
	if _, ok := b.m[id]; !ok {
		b.m[id] = t
		s.saveTitlesLocked()
	}
	b.mu.Unlock()
	if kind != "" {
		s.publish(ctx)
	}
}

// titleOf is who named a conversation ("" when the hub never noted it) and when it started.
func (s *Service) titleOf(id string) (by string, started time.Time) {
	b := &s.titles
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.titlesLocked(context.Background()) {
		return "", time.Time{}
	}
	if t := b.m[id]; t != nil {
		return t.By, t.Started
	}
	return "", time.Time{}
}

// titleSet records a title someone gave (by: user or assistant); a pending automatic one is not made any more.
func (s *Service) titleSet(machine, id, title, by string) {
	title = strings.Join(strings.Fields(title), " ")
	if title == "" {
		return
	}
	b := &s.titles
	b.mu.Lock()
	if !s.titlesLocked(context.Background()) {
		b.mu.Unlock()
		return
	}
	t := b.m[id]
	if t == nil {
		t = &convTitle{MachineID: machine}
		b.m[id] = t
	}
	t.Title, t.By = title, by
	s.saveTitlesLocked()
	b.mu.Unlock()
	s.publish(context.Background())
}

// renameConversation names an assistant conversation for the assistant's conversation_rename: like the app's "Rename",
// Claude's transcript gets the title too (its own lists show it); a Codex conversation's title is only the
// assistant's (the node would write Claude's custom-title line into the Codex rollout).
func (s *Service) renameConversation(ctx context.Context, c proto.Session, title, by string) error {
	if c.Agent != "codex" {
		cctx, cancel := context.WithTimeout(ctx, 30*time.Second)
		defer cancel()
		if err := s.call(cctx, c.MachineID, DeviceSessionRename, map[string]any{"session_id": c.SessionID, "title": title}, nil); err != nil {
			return err
		}
	}
	s.titleSet(c.MachineID, c.SessionID, title, by)
	return nil
}

type shownTitle struct{ title, machine string }

// conversationTitles are the titles to show, by session id; false while they could not be read.
func (s *Service) conversationTitles() (map[string]shownTitle, bool) {
	b := &s.titles
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.titlesLocked(context.Background()) {
		return nil, false
	}
	out := make(map[string]shownTitle, len(b.m))
	for id, t := range b.m {
		if t.Title != "" {
			out[id] = shownTitle{t.Title, t.MachineID}
		}
	}
	return out, true
}

// titleAfterTurn runs when an assistant conversation's turn ends: a conversation the hub started and nobody has
// named yet, or one that only has the stand-in, is named now (the hub calls it in the background).
func (s *Service) titleAfterTurn(ctx context.Context, machine, id string) {
	b := &s.titles
	b.mu.Lock()
	if !s.titlesLocked(context.Background()) {
		b.mu.Unlock()
		return
	}
	t := b.m[id]
	if t == nil || !t.awaitsName() || b.naming[id] {
		b.mu.Unlock()
		return
	}
	if s.isMain(context.Background(), id) { // it has no title: not worth a model call (#1313)
		delete(b.m, id)
		s.saveTitlesLocked()
		b.mu.Unlock()
		return
	}
	if b.naming == nil {
		b.naming = map[string]bool{}
	}
	b.naming[id] = true
	started := t.Started
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), titleBudget)
	defer cancel()
	first, reply := s.firstExchange(ctx, machine, id)
	title, final := s.makeTitle(ctx, started, first, reply)

	b.mu.Lock()
	delete(b.naming, id)
	if t := b.m[id]; t != nil && t.awaitsName() { // the user may have renamed it meanwhile
		t.Title, t.By = title, titleAuto
		if final {
			t.Tries = 0
		} else {
			t.Tries++
		}
		s.saveTitlesLocked()
	}
	b.mu.Unlock()
	if final {
		s.logf("assistant conversation %s: titled %q", short(id), title)
	} else {
		s.logf("assistant conversation %s: titled %q for now, the model was out of reach (named again after a later turn)", short(id), title)
	}
	s.publish(context.Background())
}

// firstExchange is the conversation's first message and the answer to it: its first turn, read from the start (a
// stand-in title is named again after a later turn, when the newest turn is another one).
func (s *Service) firstExchange(ctx context.Context, machine, id string) (first, reply string) {
	var res struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "after_seq": 0, "lazy_images": true}, &res); err != nil {
		s.logf("title %s: %v", short(id), err)
	}
	for _, it := range res.Items {
		if it.Kind != proto.KindText || strings.TrimSpace(it.Text) == "" {
			continue
		}
		if it.Role == "user" && !it.Queued { // a prompt: the first opens the turn, the next one ends it
			if first != "" {
				break
			}
			first = it.Text
		} else if it.Role == "assistant" && first != "" {
			reply = it.Text
		}
	}
	if first == "" { // the list has the first message as the title of a conversation nobody named
		if c, ok := s.session(ctx, machine, id); ok {
			first = c.Title
		}
	}
	// A conversation another one started (conversation_new) opens with a note on where it comes from: not its topic.
	first = strings.TrimSpace(contextBlock.ReplaceAllString(first, ""))
	return cutRunes(first, 1500), cutRunes(wire.AssistantCardText(reply), 1500)
}

// makeTitle asks the model for "kind | topic" and falls back to "explore | the first message's first 14 characters".
// Each way an answer can go wrong gets one more call, within the naming's time: no answer (the call ran out of time,
// a gateway error: ai.Transient), a reply stopped at max_tokens (half a title even when it has the format: the
// thinking used the room, so twice the room) and an answer that breaks the format (told what was wrong). final is
// false when the naming ended for want of an answer rather than because of one (also when there was no first message
// to read): the title is a stand-in then, and a later turn names the conversation again.
func (s *Service) makeTitle(ctx context.Context, started time.Time, first, reply string) (title string, final bool) {
	reply = wire.AssistantCardText(reply)
	day := titleDay(started, s.location(ctx))
	fallback := func(final bool) (string, bool) {
		topic := titleTopic(first)
		if topic == "" {
			topic = "新对话"
		}
		return day + " | " + titleFallback + " | " + topic, final
	}
	if strings.TrimSpace(first) == "" {
		return fallback(false)
	}
	locale := s.hello(ctx).Locale
	retry, max, room := "", titleTopicMax(locale), titleMaxTokens
	system := prompts.RenderSessionTitle(prompts.SessionTitle{Language: answerLanguage(locale), TopicMax: max})
	model := s.purposeModel("assistant.title")
	var failed, cut, misformed bool // each once
	for {
		cctx, cancel := context.WithTimeout(ctx, titleCallTimeout)
		input := prompts.RenderSessionTitleInput(prompts.SessionTitleInput{First: first, Reply: reply, Retry: retry})
		r, err := model.Chat(cctx, ai.Request{System: system, Messages: []ai.Message{{Role: ai.RoleUser, Content: input}}, MaxTokens: room})
		cancel()
		switch {
		case err != nil && ai.Transient(err) && !failed && ctx.Err() == nil:
			s.logf("title: %v (asking once more)", err)
			failed = true
			continue
		case err != nil: // a model that isn't set up or turned it down answers the same next time: that's final
			if !ai.IsPaused(err) {
				s.logf("title: %v", err)
			}
			return fallback(!ai.Transient(err) && !ai.IsPaused(err))
		case r.Truncated && !cut:
			cut, room = true, room*2
			continue
		case r.Truncated:
			return fallback(false)
		}
		kind, topic, ok := parseTitleMax(r.Text, max)
		if ok {
			return day + " | " + kind + " | " + topic, true
		}
		if misformed {
			if kind != "" { // a known kind and a topic that is still too long: cut it
				return day + " | " + kind + " | " + cutRunes(topic, max), true
			}
			return fallback(true)
		}
		misformed, retry = true, cutRunes(oneLine(r.Text), 60)
	}
}

// titleDay is MMDD of t in the user's time zone (the hub runs in UTC: 00:30 in Shanghai is the day before there).
func titleDay(t time.Time, loc *time.Location) string { return t.In(loc).Format("0102") }

var titleDate = lazyre.New(`^\d{4}\s*$`)

// titleTopicMax is how many characters a topic in the user's language may have: 14 in Chinese (and when the hub says
// no language), more where a word takes more characters.
func titleTopicMax(locale string) int {
	switch l := i18n.Lang(locale); {
	case !i18n.Valid(l) || l == i18n.ZhHans:
		return maxTitleTopic
	case l == i18n.Ja:
		return 20
	default:
		return 32
	}
}

// parseTitle is parseTitleMax for a Chinese topic.
func parseTitle(out string) (kind, topic string, ok bool) { return parseTitleMax(out, maxTitleTopic) }

// parseTitleMax reads "kind | topic" from what the model said (one line; a date it added anyway, quotes, a full-width
// bar are tolerated). kind is "" when it isn't one of titleKinds; ok when the topic fits in max characters too.
func parseTitleMax(out string, max int) (kind, topic string, ok bool) {
	line := ""
	for l := range strings.SplitSeq(out, "\n") {
		if l = strings.TrimSpace(l); l != "" {
			line = l
			break
		}
	}
	line = strings.Trim(line, "「」\"'“”` ")
	parts := strings.FieldsFunc(line, func(r rune) bool { return r == '|' || r == '｜' })
	for len(parts) > 2 && titleDate.MatchString(strings.TrimSpace(parts[0])) {
		parts = parts[1:]
	}
	if len(parts) != 2 {
		return "", "", false
	}
	kind = strings.TrimSpace(parts[0])
	if !slices.Contains(titleKinds, kind) {
		kind = ""
	}
	topic = strings.Trim(strings.Join(strings.Fields(parts[1]), " "), "「」\"'“”。.，, ")
	n := utf8.RuneCountInString(topic)
	return kind, topic, kind != "" && n > 0 && n <= max
}

var contextBlock = lazyre.New(`(?s)<` + compat.ContextPattern() + `\b[^>]*>.*?</` + compat.ContextPattern() + `>`)

var (
	// A whole tag block a source channel wraps text in (an at mention or font tag from a card, an older node's
	// injected context block): nothing inside it is part of the title.
	titleTagBlock = lazyre.New(`(?s)<[a-zA-Z][\w-]*[^>]*>.*?</[a-zA-Z][\w-]*>`)
	// Markdown punctuation that is only syntax at the start of a line: an ATX heading, a quote, a list bullet and
	// the clock a scheduled task opens with. The same character mid-line is text: C#, A*, #3471, a>b (#3869). The
	// anchored group repeats, so a whole prefix chain (">>>", "> - x", "- # x") goes in one linear-time match.
	titleLineHead = lazyre.New(`(?m)^[ \t]*(?:[ \t]{0,3}#{1,6}[ \t]+|[ \t]*>+[ \t]?|[ \t]*[-*+][ \t]+|[ \t]*⏰)+`)
	// A paired inline wrap is unwrapped and the text inside is kept; a lone marker is not a wrap and stays.
	titleCodeWrap = lazyre.New("`([^`\n]+)`")
	titleBoldWrap = lazyre.New(`\*\*([^*\n]+)\*\*`)
)

// cleanTitleNoise drops the tag blocks and markdown wrapping a title comes with, without deleting characters that
// are part of the text (# in C#, * in A*, a #-number, > in a>b, a path fragment): line syntax is only taken at the
// start of a line, inline markers only when they pair around text.
func cleanTitleNoise(s string) string {
	s = titleTagBlock.ReplaceAllString(s, " ")
	s = titleCodeWrap.ReplaceAllString(s, "$1")
	s = titleBoldWrap.ReplaceAllString(s, "$1")
	return titleLineHead.ReplaceAllString(s, "")
}

// titleTopic is at most maxTitleTopic characters of s on one line: a task's name, or the fallback from the first
// message (without the tags and markdown around what the user wrote).
func titleTopic(s string) string {
	s = strings.Join(strings.Fields(cleanTitleNoise(s)), " ")
	s = strings.Trim(s, "，。,.：:；; ")
	return cutRunes(s, maxTitleTopic)
}

func cutRunes(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return strings.TrimSpace(string(r[:n]))
	}
	return s
}
