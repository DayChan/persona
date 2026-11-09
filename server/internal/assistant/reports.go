package assistant

import (
	"context"
	"encoding/json"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Task reports in Feishu (#2768, docs/extensions/bots.md "Task progress"): the user reads the bot's chat, not the app, so how the
// main conversation's tasks go — done, waiting for the user, cut off, did not start, closed by the judge — is told
// there by the bot itself (no lark-cli, nothing to approve per message). Changes within reportDelay are one card:
// what needs the user's decision on top (each with its own answerable card below it), then each task with its stage
// (plan → develop → review → merge → deploy → verified), a line of what happened and links to its page, its PR and its agent.
// The bot's reports switch turns it off (on by default). With only_conclusions on (#1226) the card carries only how tasks
// ended (closed, did not start): a finished turn, waiting and interrupted stay in the task list and the briefing, and a
// decision waiting still gets its own answerable card. A supervised task's review sends its conclusion card instead (conclusion.go).

// reportDelay is how long a change waits for others before the card goes out (reportBook.delay overrides it).
const reportDelay = time.Minute

// reportRetryDelay is how long a batch Feishu could not take waits before it is sent again (reportBook.retry
// overrides it). reportMaxRetries caps the retries after the first attempt: then the batch is given up and logged,
// so a dead transport cannot pin the queue (and terminal reports) forever.
const (
	reportRetryDelay = 30 * time.Second
	reportMaxRetries = 3
	// reportCardMaxBytes/Elements bound one card; each item's real marshaled JSON is measured against them.
	reportCardMaxBytes    = 24000
	reportCardMaxElements = 50
	reportFallbackMax     = 1500 // runes of a card's rich-text fallback (PostMarkdown clips neither)
)

// Report states, in the order a card lists them after the decisions.
const (
	reportNeeds       = "needs"
	reportFailed      = "failed"
	reportInterrupted = "interrupted"
	reportDone        = "done"
	reportClosed      = "closed"
)

var reportOrder = map[string]int{reportNeeds: 0, reportFailed: 1, reportInterrupted: 2, reportDone: 3, reportClosed: 4}

// Stages of a task, in order (Todo.Stage; the apps draw the same bar).
var taskStages = []string{"plan", "develop", "review", "merge", "deploy", "verify"}

func stageIndex(s string) int {
	for i, x := range taskStages {
		if x == s {
			return i
		}
	}
	return -1
}

var (
	deployWords = lazyre.New(`(?i)\bdeployed\b|\bis live\b|\bwent live\b|已部署|已经部署|完成部署|部署好|部署了|部署完|部署到|部署上线|上线了|已上线|发布了|已发布|发版了`)
	verifyWords = lazyre.New(`(?i)\bverified\b|已验证|验证通过|实测通过|实测.{0,6}(通过|成功|收到)|截图.{0,4}(确认|为证)`)
	// A deploy or verify phrase does not count when, in its own clause, the few words right before it negate or defer
	// it ("not deployed to … yet", "don't deploy yet", "deploy after you confirm", not yet deployed, it will be deployed) or what follows asks or says
	// "yet" ("deployed?", deployed yet): a false "deployed" would ask the user to check something that is not there.
	notYetZh = lazyre.New(`没|未|不|别|无需|暂不|无法|待|等|还要|需要|准备`)
	notYetEn = lazyre.New(`(?i)\b(not|never|nothing|before|will|once|awaiting|pending)\b|n't\b|\byet to\b|\bto be\b`)
	askAfter = lazyre.New(`(?i)^\s*(吗|么|没有?|与否|\?|？|yet\b)`)
)

// clauseMarks end a clause: a negation before one of them is about something else.
const clauseMarks = "，。；：,.;:!?！？\n"

// said: re matches text somewhere it is told as done, not negated, deferred or asked.
func said(re *lazyre.Regexp, text string) bool {
	for _, m := range re.FindAllStringIndex(text, -1) {
		if askAfter.MatchString(text[m[1]:]) || negatedBefore(text[:m[0]]) {
			continue
		}
		return true
	}
	return false
}

// negatedBefore: the end of pre, within its last clause, negates or defers what comes next (8 characters for
// Chinese, 3 words for English).
func negatedBefore(pre string) bool {
	if i := strings.LastIndexAny(pre, clauseMarks); i >= 0 {
		_, w := utf8.DecodeRuneInString(pre[i:])
		pre = pre[i+w:]
	}
	tail := pre
	for i, n := len(pre), 0; i > 0; n++ {
		if n == 8 {
			tail = pre[i:]
			break
		}
		_, w := utf8.DecodeLastRuneInString(pre[:i])
		i -= w
	}
	words := strings.Fields(pre)
	if len(words) > 3 {
		words = words[len(words)-3:]
	}
	return notYetZh.MatchString(tail) || notYetEn.MatchString(strings.Join(words, " "))
}

// stageOf is how far a reply says the work got (at least develop: the agent worked), and the PR it names.
func stageOf(text string) (stage, pr string) {
	stage = "develop"
	if prs := prLink.FindAllString(text, -1); len(prs) > 0 {
		pr = prs[len(prs)-1]
		stage = "review"
		if mergedWords.MatchString(text) {
			stage = "merge"
		}
	}
	if said(deployWords, text) {
		stage = "deploy"
	}
	if stage == "deploy" && said(verifyWords, text) {
		stage = "verify" // checked on what was deployed
	}
	return stage, pr
}

type reportItem struct {
	todo    string
	state   string
	machine string // a name
	session [2]string
	text    string
	ix      *proto.Interaction
	at      time.Time
	tries   int // sends already attempted for this change (0: the first send is pending)
}

type reportBook struct {
	mu      sync.Mutex
	sending sync.Mutex             // serializes sendReports: a later timer must not take a batch while a post is still in flight
	pending map[string]*reportItem // by todo id: the latest change of each
	timer   *time.Timer
	stages  map[string]todoStage // by todo id (reportStagesKey)
	loaded  bool
	delay   time.Duration // 0: reportDelay (the tests' is shorter)
	retry   time.Duration // 0: reportRetryDelay (the tests' is shorter)
}

type todoStage struct {
	Stage    string        `json:"stage"`
	PR       string        `json:"pr,omitempty"`
	Delivery *TodoDelivery `json:"delivery,omitempty"` // delivery.go
}

const reportStagesKey = "assistant.todo_stages"

// taskReport notes a change of task t for the next card. ix: what it asks (needs).
func (s *Service) taskReport(t Todo, state, machine string, session [2]string, text string, ix *proto.Interaction) {
	if t.Origin == "" {
		return
	}
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = map[string]*reportItem{}
	}
	b.pending[t.ID] = &reportItem{todo: t.ID, state: state, machine: machine, session: session, text: text, ix: ix, at: time.Now()}
	if b.timer == nil {
		d := b.delay
		if d <= 0 {
			d = reportDelay
		}
		b.timer = time.AfterFunc(d, s.sendReports)
	}
}

// setTaskStage moves task id to stage (never back) with its PR, kept in the store and on the todo.
func (s *Service) setTaskStage(ctx context.Context, id, stage, pr string) {
	b := &s.reports
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		s.logf("task %s: stage %s not kept: the stages could not be read", id, stage)
		return
	}
	cur := b.stages[id]
	changed := false
	if stageIndex(stage) > stageIndex(cur.Stage) {
		cur.Stage, changed = stage, true
	}
	if pr != "" && pr != cur.PR {
		cur.PR, changed = pr, true
	}
	if changed {
		b.stages[id] = cur
		if st := s.store(); st != nil {
			if err := st.PutSetting(ctx, reportStagesKey, b.stages); err != nil {
				s.logf("task %s: stage: %v", id, err)
			}
		}
	}
	b.mu.Unlock()
	if !changed {
		return
	}
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.Stage, t.PRURL = cur.Stage, cur.PR
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
}

// loadStagesLocked reads the stages once and reports whether they are in memory; s.reports.mu is held. A failed read
// leaves them unloaded (the next look tries again): writing the book after it would put one task's stage over every
// other task's stage, PR and delivery.
func (s *Service) loadStagesLocked(ctx context.Context) bool {
	b := &s.reports
	if b.loaded {
		return true
	}
	stages := map[string]todoStage{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, reportStagesKey, &stages); err != nil {
			s.logf("todo stages: %v", err)
			return false
		}
		if stages == nil {
			stages = map[string]todoStage{}
		}
	}
	b.stages, b.loaded = stages, true
	return true
}

// todoStages are the kept stages, for the todos as they are read.
func (s *Service) todoStages(ctx context.Context) map[string]todoStage {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadStagesLocked(ctx) {
		return map[string]todoStage{}
	}
	out := make(map[string]todoStage, len(b.stages))
	for k, v := range b.stages {
		out[k] = v
	}
	return out
}

// sendReports sends the changes noted since the last card: one card, the decisions first. Nothing goes out while the
// bot is off, unpaired or its reports switch is off (the changes are dropped: a later card says how things are then).
// A transport failure is not a drop: the batch goes back to pending and is retried (requeueReports).
func (s *Service) sendReports() {
	b := &s.reports
	// Hold sending for the whole post: a normal-delay timer that fires while a long post is still in flight waits
	// here, so its run cannot snapshot the newer items a failed run needs to merge with.
	b.sending.Lock()
	defer b.sending.Unlock()
	b.mu.Lock()
	items := make([]*reportItem, 0, len(b.pending))
	for _, it := range b.pending {
		items = append(items, it)
	}
	b.pending, b.timer = nil, nil
	b.mu.Unlock()
	if len(items) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, ready := s.feishuFeatures(ctx)
	if !ready || !f.Reports {
		return
	}
	var keep, asks []*reportItem // asks: decisions whose own card goes out without a line in the report (only_conclusions)
	for _, it := range items {
		t, ok := s.todoNow(it.todo)
		if !ok {
			continue // deleted meanwhile
		}
		if it.state == reportNeeds && !s.taskWaits(ctx, t) {
			continue // answered meanwhile
		}
		if it.state == reportNeeds && it.ix != nil && s.botCardOut(it.ix.ID) {
			continue // its question card is in the chat already (cards / all_cards): that says it waits
		}
		if t.Quiet && f.Sync && it.state != reportClosed {
			continue // told in the main conversation, whose turns the chat mirrors (sync); closing is not told there
		}
		if f.OnlyConclusions && !reportConclusion(it.state) {
			if it.state == reportNeeds && it.ix != nil {
				asks = append(asks, it) // the decision itself still reaches the owner
			}
			continue
		}
		keep = append(keep, it)
	}
	if len(keep) == 0 {
		s.postReportAsks(asks)
		return
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if a, b := reportOrder[keep[i].state], reportOrder[keep[j].state]; a != b {
			return a < b
		}
		if !keep[i].at.Equal(keep[j].at) {
			return keep[i].at.Before(keep[j].at)
		}
		return keep[i].todo < keep[j].todo // identical timestamps stay in a stable, repost-safe order
	})
	cards, fallbacks := s.reportCards(ctx, keep)
	// A notifications companion takes the report (feishu_companions.go); the decisions' own cards, which answer for
	// the owner, still go to the chat below.
	if s.divertNotices(ctx, "report", cards, fallbacks) {
		s.postReportAsks(append(asks, keep...))
		return
	}
	// Every card must land: a failure after some went out requeues the whole batch (reposts early ones, retry-capped).
	for i, c := range cards {
		if _, err := s.botPost(ctx, "", c, fallbacks[i]); err != nil {
			s.logf("bots: report: %v", err)
			s.requeueReports(append(keep, asks...)) // the decisions held back from the card are not lost with it
			return
		}
	}
	s.postReportAsks(append(asks, keep...))
}

// postReportAsks: a decision to make gets its own card with the choices, answerable there, unless one is already in the chat.
func (s *Service) postReportAsks(items []*reportItem) {
	for _, it := range items {
		if it.state == reportNeeds && it.ix != nil && !s.botCardOut(it.ix.ID) {
			s.feishuInteractionAlways(*it.ix)
		}
	}
}

// reportConclusion says a report state tells how a task ended (closed by the judge, or did not start), the lines
// only_conclusions keeps.
func reportConclusion(state string) bool { return state == reportClosed || state == reportFailed }

// dropTaskReport forgets task id's change still waiting for the next card: its conclusion card tells how it ended.
func (s *Service) dropTaskReport(id string) {
	b := &s.reports
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// requeueReports puts a batch the transport could not deliver back for a short-delayed retry. It keeps the semantic
// drops sendReports already made (deleted, answered, quiet-synced items are not in the batch). A change of the same
// todo that arrived while the send was in flight replaces the failed one when it is newer; items that have used
// reportMaxRetries retries are given up (and logged). Whatever is pending afterwards — requeued or newly arrived —
// gets one timer.
func (s *Service) requeueReports(items []*reportItem) {
	b := &s.reports
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]*reportItem{}
	}
	var gaveUp []string
	for _, it := range items {
		if cur, ok := b.pending[it.todo]; ok && cur.at.After(it.at) {
			continue // a newer change of this todo arrived meanwhile
		}
		if it.tries >= reportMaxRetries {
			gaveUp = append(gaveUp, it.todo)
			continue
		}
		it.tries++
		b.pending[it.todo] = it
	}
	if len(b.pending) > 0 {
		if b.timer != nil {
			b.timer.Stop() // a new change's normal-delay timer: the failed batch gets the short retry delay
		}
		d := b.retry
		if d <= 0 {
			d = reportRetryDelay
		}
		b.timer = time.AfterFunc(d, s.sendReports)
	}
	b.mu.Unlock()
	for _, id := range gaveUp {
		s.logf("bots: report: giving up task %s after %d failed sends", id, reportMaxRetries+1)
	}
}

// botCardOut: the bot has posted (or is posting) a card for interaction id.
func (s *Service) botCardOut(id string) bool {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	_, ok := b.cards[id]
	return ok
}

type reportEntry struct {
	t       Todo
	md      string
	buttons []feishubot.Button
	bytes   int // marshaled JSON length of its Markdown block plus button row
}

func (s *Service) reportCards(ctx context.Context, items []*reportItem) ([]*feishubot.Card, []string) {
	l, web, stages := s.botLang(ctx), s.webBase(ctx), s.todoStages(ctx)
	es, needs := make([]reportEntry, len(items)), 0
	for i, it := range items {
		if it.state == reportNeeds {
			needs++
		}
		t, _ := s.todoNow(it.todo)
		md, buttons := s.reportItemView(l, web, stages, t, it)
		es[i] = reportEntry{t: t, md: md, buttons: buttons}
		es[i].bytes = feishuElBytes(func(c *feishubot.Card) {
			c.Markdown(md)
			if len(buttons) > 0 {
				c.Buttons(buttons...)
			}
		})
	}
	head1, headN := reportCardHeads(l, needs, len(items))
	hr := feishuElBytes(func(c *feishubot.Card) { c.HR() })
	segs := reportCardSegments(es, head1, headN, hr)
	cards, fallbacks := make([]*feishubot.Card, 0, len(segs)), make([]string, 0, len(segs))
	offset := 0
	for k, seg := range segs {
		c, fb := reportCardOne(l, seg, k, len(segs), offset, needs, len(items))
		cards, fallbacks, offset = append(cards, c), append(fallbacks, fb), offset+len(seg)
	}
	return cards, fallbacks
}

// feishuElBytes is the JSON length of add()'s elements; json.Marshal escapes &<>, Card.JSON does not, so it errs high.
func feishuElBytes(add func(*feishubot.Card)) int {
	c := feishubot.NewCard("", "", "")
	add(c)
	n := 0
	for _, el := range c.Body.Elements {
		b, _ := json.Marshal(el)
		n += len(b) + 1 // its joining comma inside the body's element array
	}
	return n
}

// reportCardHeads measures the first card (longest title, both section labels charged) and the plain later cards.
func reportCardHeads(l i18n.Lang, needs, total int) (head1, headN int) {
	part := i18n.T(l, "assistant-reports.part", i18n.Args{"k": total, "n": total})
	first := i18n.T(l, "assistant-reports.title", i18n.Args{"n": total}) + part
	if needs > 0 {
		first = i18n.T(l, "assistant-reports.title_needs", i18n.Args{"n": needs}) + part
	}
	c1 := feishubot.NewCard("📋 "+first, feishubot.Orange, first)
	c1.Markdown("**" + i18n.T(l, "assistant-reports.decide") + "**")
	c1.Markdown("**" + i18n.T(l, "assistant-reports.progress") + "**")
	plain := i18n.T(l, "assistant-reports.title", i18n.Args{"n": total}) + part
	cN := feishubot.NewCard("📋 "+plain, feishubot.Blue, plain)         // suffix measured with the longest part too
	cN.Markdown("**" + i18n.T(l, "assistant-reports.progress") + "**") // a progress label may land past card 1
	return len(c1.JSON()), len(cN.JSON())
}

// reportCardSegments packs entries within the budgets; an item is never cut, one always fits on an empty card.
func reportCardSegments(es []reportEntry, head1, headN, hr int) [][]reportEntry {
	segs := [][]reportEntry{{}}
	ne, nb := 2, head1 // the first card reserves two element slots for the section labels
	for _, e := range es {
		ie, ib := 1, e.bytes
		if len(e.buttons) > 0 {
			ie++ // its button row
		}
		cur := &segs[len(segs)-1]
		if len(*cur) > 0 {
			ie, ib = ie+1, ib+hr // HR before a later item
			if ne+ie > reportCardMaxElements || nb+ib > reportCardMaxBytes {
				segs = append(segs, nil)
				cur, ne, nb, ie, ib = &segs[len(segs)-1], 1, headN, ie-1, ib-hr
			}
		}
		*cur, ne, nb = append(*cur, e), ne+ie, nb+ib
	}
	return segs
}

// reportCardOne builds one segment; k is its 0-based index of n, off its first item's global position.
func reportCardOne(l i18n.Lang, seg []reportEntry, k, n, off, needs, total int) (*feishubot.Card, string) {
	title, template := i18n.T(l, "assistant-reports.title", i18n.Args{"n": total}), feishubot.Blue
	if needs > 0 && k == 0 { // only the first card carries the decisions
		title, template = i18n.T(l, "assistant-reports.title_needs", i18n.Args{"n": needs}), feishubot.Orange
	}
	if n > 1 {
		title += i18n.T(l, "assistant-reports.part", i18n.Args{"k": k + 1, "n": n})
	}
	c := feishubot.NewCard("📋 "+title, template, title)
	titles := make([]string, len(seg))
	for i, e := range seg {
		if i > 0 {
			c.HR()
		}
		if g := off + i; g == 0 && needs > 0 {
			c.Markdown("**" + i18n.T(l, "assistant-reports.decide") + "**")
		} else if g == needs && needs > 0 {
			c.Markdown("**" + i18n.T(l, "assistant-reports.progress") + "**")
		}
		titles[i] = clip(oneLine(e.t.Title), 40) // same clip as the body, so every visible item fits the fallback
		c.Markdown(e.md)
		if len(e.buttons) > 0 {
			c.Buttons(e.buttons...)
		}
	}
	return c, title + "：" + clip(strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep")), reportFallbackMax)
}

// reportItemView renders one item's Markdown body and its link buttons (task page, PR, agent chat).
func (s *Service) reportItemView(l i18n.Lang, web string, stages map[string]todoStage, t Todo, it *reportItem) (string, []feishubot.Button) {
	var md strings.Builder
	md.WriteString(i18n.T(l, "assistant-reports.state_"+it.state) + " **" + feishubot.EscapeMarkdown(clip(oneLine(t.Title), 40)) + "**")
	if it.machine != "" {
		md.WriteString(" · " + feishubot.EscapeMarkdown(it.machine))
	}
	md.WriteString("\n" + stageBar(l, stages[t.ID].Stage))
	if text := strings.TrimSpace(it.text); text != "" {
		md.WriteString("\n" + feishubot.CardMarkdown(clipMarkdown(text, 300)))
	}
	if it.state == reportNeeds {
		md.WriteString("\n" + i18n.T(l, "assistant-reports.answer_below"))
	}
	var bs []feishubot.Button
	if link := todoLink(web, t.ID); link != "" {
		bs = append(bs, feishubot.LinkBtn(i18n.T(l, "assistant-reports.open_task"), link))
	}
	if pr := stages[t.ID].PR; pr != "" {
		bs = append(bs, feishubot.LinkBtn(i18n.T(l, "assistant-reports.open_pr"), pr))
	}
	if it.session[1] != "" && web != "" {
		bs = append(bs, feishubot.LinkBtn(i18n.T(l, "assistant-reports.open_agent"), sessionLink(web, it.session[0], it.session[1], false)))
	}
	return md.String(), bs
}

// stageBar draws the stages with the one reached marked: ✅ before it, 🔵 on it, ⚪ after (none known: all ⚪).
func stageBar(l i18n.Lang, stage string) string {
	at := stageIndex(stage)
	parts := make([]string, len(taskStages))
	for i, st := range taskStages {
		mark := "⚪"
		switch {
		case i < at:
			mark = "✅"
		case i == at:
			mark = "🔵"
		}
		name := i18n.T(l, "assistant-reports.stage_"+st)
		if i == at {
			name = "**" + name + "**"
		}
		parts[i] = mark + name
	}
	return strings.Join(parts, " → ")
}

// taskAsks notes a card of a task's agent (not a quiet one: InteractionRaised notes those) for the next report.
func (s *Service) taskAsks(ix proto.Interaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t, ok := s.taskOf(ctx, ix.MachineID, ix.SessionID)
	if !ok || t.Quiet {
		return
	}
	name := ix.MachineID
	if m, ok := s.device(ctx, ix.MachineID); ok {
		name = m.Name
	}
	s.taskReport(t, reportNeeds, name, [2]string{ix.MachineID, ix.SessionID}, noticeAsk(ix), &ix)
}

// taskClosedReport: the judge closed task t (reason: why). Its done report still waiting becomes this one, keeping
// what the agent said; else the reason is the line.
func (s *Service) taskClosedReport(t Todo, reason string) {
	b := &s.reports
	b.mu.Lock()
	if cur, ok := b.pending[t.ID]; ok && cur.state == reportDone {
		cur.state = reportClosed
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	s.taskReport(t, reportClosed, "", [2]string{}, reason, nil)
}
