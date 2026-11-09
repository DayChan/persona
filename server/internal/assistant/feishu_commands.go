package assistant

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// --- commands (docs/extensions/bots.md "Commands"): which conversation or agent messages go to, the lists to pick one from,
// starting an agent, stopping a turn ---

func botHelp(l i18n.Lang, f FeishuFeatures) string {
	var b strings.Builder
	b.WriteString(i18n.T(l, "assistant-feishu_cards.help_intro"))
	if f.Main {
		b.WriteString(i18n.T(l, "assistant-feishu_cards.help_main"))
	}
	if f.Agents {
		b.WriteString(i18n.T(l, "assistant-feishu_cards.help_agents"))
	}
	b.WriteString(i18n.T(l, "assistant-feishu_cards.help_tasks"))
	if f.Workbench {
		b.WriteString(i18n.T(l, "assistant-feishu_cards.help_workbench"))
	}
	b.WriteString(i18n.T(l, "assistant-feishu_cards.help_always"))
	return b.String()
}

// parseBotCommand splits a bot message into its /command, lower-cased, and the rest. Any white space ends the command
// (a line break, a tab, a full-width space), a full-width slash starts one like "/", and the rest keeps its own line
// breaks with only the white space around it trimmed. ok is false when the message does not start with a slash.
func parseBotCommand(text string) (cmd, rest string, ok bool) {
	t := strings.TrimLeftFunc(text, unicode.IsSpace)
	if after, found := strings.CutPrefix(t, "／"); found {
		t = "/" + after
	}
	if !strings.HasPrefix(t, "/") {
		return "", "", false
	}
	end := strings.IndexFunc(t, unicode.IsSpace)
	if end < 0 {
		end = len(t)
	}
	return strings.ToLower(t[:end]), strings.TrimFunc(t[end:], unicode.IsSpace), true
}

// botCommand runs a /command. Not handled, the message goes on with the text returned: as it is for a command it does
// not know (it may be the agent's own, like /compact), without the command for "/new <message>". root and parent are
// the message's thread root and the message it replies to: the commands that act on a conversation follow the target
// the thread binds, like a normal message does.
func (s *Service) botCommand(ctx context.Context, f FeishuFeatures, messageID, text, root, parent string) (bool, string) {
	l := s.botLang(ctx)
	cmd, rest, ok := parseBotCommand(text)
	if !ok {
		return false, text
	}
	// fallback goes as a rich-text list when Feishu refuses the card (the list cards carry their own; other command
	// replies have none).
	reply := func(c *feishubot.Card, fallback string) {
		if _, err := s.botPost(ctx, messageID, c, fallback); err != nil {
			s.logf("bots: command reply: %v", err)
		}
	}
	switch compat.LegacyBotCommand(cmd) {
	case "/help", "/帮助":
		reply(feishubot.NewCard(i18n.T(l, "assistant-feishu_cards.help_title"), feishubot.Blue, i18n.T(l, "assistant-feishu_cards.help_title")).Markdown(botHelp(l, f)), "")
	case "/main", "/persona", "/主对话":
		if !f.Main {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.main_off"))
			return true, ""
		}
		s.setBotCurrent(botTarget{Kind: targetMain})
		s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.now_to_main"))
	case "/new", "/新对话":
		if !f.Main {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.main_off"))
			return true, ""
		}
		s.setBotCurrent(botTarget{Kind: targetPersona})
		if rest == "" {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.next_is_new"))
			return true, ""
		}
		return false, rest // "/new help me…": the rest starts it
	case "/list", "/对话":
		if !f.Main {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.main_off"))
			return true, ""
		}
		reply(s.conversationsCard(ctx))
	case "/agents", "/agent":
		if !f.Agents {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.agents_off"))
			return true, ""
		}
		reply(s.agentsCard(ctx, rest))
	case "/start", "/开工":
		if !f.Agents {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.agents_off"))
			return true, ""
		}
		reply(s.startAgentCard(ctx, rest), "")
	case "/stop", "/停止", "/停":
		out := s.stopConversation(ctx, f, s.botTargetFor(root, parent))
		s.botNote(ctx, messageID, out.message)
		return true, ""
	case "/status", "/状态":
		t := s.botTargetFor(root, parent)
		if err := botAllowed(l, f, t); err != nil {
			s.botNote(ctx, messageID, err.Error())
			return true, ""
		}
		card, fallback := s.statusCard(ctx, t)
		reply(card, fallback)
	case "/tasks", "/进展", "/总览": // i18n-ignore: command aliases typed by the user; the catalogs describe them in /help
		if f.Board && s.standingBoardCommand(ctx) {
			break
		}
		for _, oc := range s.tasksOverview(ctx) {
			reply(oc.card, oc.fallback)
		}
	default:
		if s.workbenchCommand(ctx, f, cmd) { // /home, /today, /inbox with the workbench on (feishu_workbench.go)
			return true, ""
		}
		// A command the bot does not know goes on as it is (an agent's own, like /compact). A bare one a letter off
		// one of the bot's (/stpo for /stop) sent to a Persona conversation would sit in line behind the turn it
		// meant to stop and be answered: the owner is asked instead. Words after it (/news what is in the news today) make it a
		// message; an agent's may be the project's own (/lint), so it goes. A doubled slash (//stpo) sends the text
		// itself, one slash off, but only before a bot command or a slip of one and outside an agent: other text that
		// starts with // (a "// TODO" note, "//example.com/x") goes as written.
		toAgent := s.botTargetFor(root, parent).agent()
		if word, ok := strings.CutPrefix(cmd, "/"); ok && strings.HasPrefix(word, "/") {
			if literal, ok := strings.CutPrefix(strings.TrimLeftFunc(text, unicode.IsSpace), "/"); ok && !toAgent && (slices.Contains(botCommandNames, word) || botCommandTypo(word) != "") {
				return false, literal
			}
			return false, text
		}
		if want := botCommandTypo(cmd); want != "" && rest == "" && !toAgent {
			s.botNote(ctx, messageID, i18n.T(l, "assistant-feishu_cards.command_typo", i18n.Args{"got": cmd, "want": want}))
			return true, ""
		}
		return false, text
	}
	return true, ""
}

// statusPendingMax bounds a /status card's pending rows so a long queue can't get the card refused.
const statusPendingMax = 5

var ixIcons = map[string]string{proto.IxQuestion: "❓ ", proto.IxPlan: "📋 ", proto.IxPermission: "🔐 ", proto.IxScreen: "🖥 ", proto.IxElicitation: "📝 "}

// pendingQuestion is one line saying what a pending interaction waits for; "" when it says nothing.
func pendingQuestion(ix proto.Interaction) string {
	switch ix.Kind {
	case proto.IxQuestion:
		return strings.TrimSpace(questionSummary(ix))
	case proto.IxPlan:
		return strings.TrimSpace(planTitle(ix.Plan))
	case proto.IxPermission:
		return strings.TrimSpace(toolDisplayName(ix.ToolName) + " " + oneLine(ix.Summary))
	}
	return strings.TrimSpace(oneLine(ix.Summary))
}

// statusCard is /status plus the same content as plain text (the fallback when Feishu refuses the card): the
// target's work state (offline / unknown never reads as idle), last activity, goal and pending questions. With the
// fleet online both sources are local; the web button is built once and is nil without a public_url.
func (s *Service) statusCard(ctx context.Context, t botTarget) (*feishubot.Card, string) {
	l := s.botLang(ctx)
	card := feishubot.NewCard("", "", i18n.T(l, "assistant-feishu_cards.status_title"))
	rt, err := s.resolveBotTarget(ctx, t) // one resolve: the target line reuses its label, not targetLabel's
	label := i18n.T(l, "assistant-feishu.not_found")
	switch {
	case err == nil && t.Kind == targetPersona && rt.isNew:
		label = i18n.T(l, "assistant-feishu.new_chat_next")
	case err == nil:
		label = rt.label
	}
	var head, fb strings.Builder
	to := i18n.T(l, "assistant-feishu_cards.status_to", i18n.Args{"target": feishubot.EscapeMarkdown(label)})
	head.WriteString(to)
	fb.WriteString(fallbackText.Replace(i18n.T(l, "assistant-feishu_cards.status_to", i18n.Args{"target": label}))) // ends its own line
	if running := len(s.channelRuns(channel.Feishu)); running > 0 {
		waiting := i18n.T(l, "assistant-feishu_cards.status_waiting", i18n.Args{"count": running})
		head.WriteString(waiting)
		fb.WriteString(fallbackText.Replace(waiting))
	}
	card.Markdown(strings.TrimSpace(head.String()))
	if err != nil || rt.isNew || rt.session.SessionID == "" {
		return card, strings.TrimSpace(fb.String())
	}

	sess := rt.session
	if list, err := s.sessions(ctx, rt.machine); err == nil {
		for _, x := range list {
			if x.SessionID == sess.SessionID {
				sess = x
				break
			}
		}
	}
	stateKey := "assistant-feishu_cards.status_state_offline" // offline, unknown or unset: never an idle machine
	switch sess.State {
	case proto.StateNeedsYou:
		stateKey = "assistant-feishu_cards.status_state_waiting"
	case proto.StateWorking:
		stateKey = "assistant-feishu_cards.status_state_working"
	case proto.StateUnread, proto.StateIdle:
		stateKey = "assistant-feishu_cards.status_state_idle"
	}
	state := i18n.T(l, stateKey)
	line := state
	fmt.Fprintf(&fb, "%s\n", fallbackText.Replace(state))
	if sess.LastText != "" {
		last := clip(oneLine(sess.LastText), 90)
		line += "\n" + feishubot.EscapeMarkdown(last)
		fb.WriteString(fallbackText.Replace(last) + "\n")
	}
	if g := sess.Goal; g != nil && g.Objective != "" && (g.Status == "" || g.Status == proto.GoalActive) {
		goal := i18n.T(l, "assistant-feishu_cards.status_goal", i18n.Args{"objective": feishubot.EscapeMarkdown(clip(oneLine(g.Objective), 90))})
		line += "\n" + goal
		fb.WriteString(fallbackText.Replace(goal) + "\n")
	}
	card.Markdown(line)

	var pending []proto.Interaction
	for _, ix := range s.pendingInteractions() {
		if ix.MachineID == rt.machine && ix.SessionID == sess.SessionID && pendingQuestion(ix) != "" {
			pending = append(pending, ix)
		}
	}
	pendingTitle := i18n.T(l, "assistant-feishu_cards.status_pending")
	card.Markdown(pendingTitle)
	fb.WriteString(fallbackText.Replace(pendingTitle) + "\n")
	if len(pending) == 0 {
		none := i18n.T(l, "assistant-feishu_cards.status_no_pending")
		card.Markdown(none)
		fb.WriteString(none + "\n")
		return card, strings.TrimSpace(fb.String())
	}
	open := s.openButton(ctx, rt.machine, sess.SessionID) // every row is the same conversation: build it once
	shown := pending
	if len(pending) > statusPendingMax {
		shown = pending[:statusPendingMax]
	}
	for _, ix := range shown {
		q := clip(pendingQuestion(ix), 120)
		card.Markdown(ixIcons[ix.Kind] + "**" + feishubot.EscapeMarkdown(q) + "**").Buttons(open)
		fmt.Fprintf(&fb, "- %s%s\n", ixIcons[ix.Kind], fallbackText.Replace(q))
	}
	if more := len(pending) - len(shown); more > 0 {
		key := "assistant-feishu_cards.status_more_pending"
		if open == nil { // no web app to send the reader to: just count them
			key = "assistant-feishu_cards.status_more_pending_no_web"
		}
		note := i18n.T(l, key, i18n.Args{"count": more})
		card.Note(note)
		fb.WriteString(note + "\n")
	}
	return card, strings.TrimSpace(fb.String())
}

// stopOutcome is what interrupting a conversation's current turn came to; the caller renders it as a command note
// (/stop) or a card toast (the progress card's Stop button).
type stopOutcome struct {
	kind    string // toast kind: success | info | error
	message string
}

// stopConversation interrupts t's current turn. The /stop command and the progress card's Stop button share it: both
// may only act on a target this chat may use (the Agents feature gates agent threads even before the normal message
// gate), and both say so when there is nothing to stop.
func (s *Service) stopConversation(ctx context.Context, f FeishuFeatures, t botTarget) stopOutcome {
	l := s.botLang(ctx)
	if err := botAllowed(l, f, t); err != nil {
		return stopOutcome{kind: "error", message: err.Error()}
	}
	rt, err := s.resolveBotTarget(ctx, t)
	if err != nil || rt.isNew {
		return stopOutcome{kind: "info", message: i18n.T(l, "assistant-feishu_cards.nothing_running")}
	}
	if err := s.call(ctx, rt.machine, "session.interrupt", map[string]any{"session_id": rt.session.SessionID}, nil); err != nil {
		return stopOutcome{kind: "error", message: i18n.T(l, "assistant-feishu_cards.not_stopped", i18n.Args{"error": clip(oneLine(err.Error()), 200)})}
	}
	return stopOutcome{kind: "success", message: i18n.T(l, "assistant-feishu_cards.stopped", i18n.Args{"label": rt.label})}
}

// botCommandNames are the bot's own commands a typo is matched against (the Chinese ones are too short to mistype).
var botCommandNames = []string{"/help", "/main", "/persona", "/new", "/list", "/agents", "/start", "/stop", "/status", "/tasks"}

// agentCommandNames are Claude Code's and Codex's own commands, and plain words, a slip off one of the bot's: they go
// on as they are.
var agentCommandNames = map[string]bool{"/stats": true, "/model": true, "/mcp": true, "/init": true, "/exit": true, "/quit": true,
	"/clear": true, "/cost": true, "/diff": true, "/plan": true, "/undo": true, "/todos": true, "/hooks": true,
	"/man": true, "/star": true, "/stat": true}

// botCommandTypo is the bot's command cmd is a slip of (a letter dropped, or two side by side swapped), "" when none
// or when cmd is an agent's own. A letter added or changed is not counted: those make real words (/news, /now,
// /step, /shop) more often than slips.
func botCommandTypo(cmd string) string {
	if agentCommandNames[cmd] {
		return ""
	}
	for _, want := range botCommandNames {
		if cmd != want && slipOf(cmd, want) {
			return want
		}
	}
	return ""
}

// slipOf: got is want with one rune dropped, or with two adjacent runes swapped.
func slipOf(got, want string) bool {
	x, y := []rune(got), []rune(want)
	switch len(y) - len(x) {
	case 0:
		var diff []int
		for i := range x {
			if x[i] != y[i] {
				diff = append(diff, i)
			}
		}
		if len(diff) != 2 {
			return false
		}
		i, j := diff[0], diff[1]
		return j == i+1 && x[i] == y[j] && x[j] == y[i]
	case 1:
		i := 0
		for i < len(x) && x[i] == y[i] {
			i++
		}
		return string(x[i:]) == string(y[i+1:])
	}
	return false
}

// botListNameMost bounds a conversation / agent / device name in a list card: the title is often a whole command
// line and the switch button carries the name again, so an unclipped name could make Feishu refuse the whole card.
const botListNameMost = 60

var fallbackText = strings.NewReplacer("`", "", "*", "", "_", "", "[", "", "]", "", "~", "", "\\", "") // strips Markdown metacharacters for the post fallback

// conversationsCard lists recent Persona conversations with a button to switch to each. It also returns the same
// list as plain text: the card goes with it as fallback, so a refused card still shows every conversation.
func (s *Service) conversationsCard(ctx context.Context) (*feishubot.Card, string) {
	list, main := s.conversations(ctx, false, "")
	sort.SliceStable(list, func(i, j int) bool { return list[i].UpdatedAt.After(list[j].UpdatedAt) })
	l := s.botLang(ctx)
	title := i18n.T(l, "assistant-feishu_cards.recent_chats")
	c := feishubot.NewCard(title, feishubot.Blue, title)
	if len(list) == 0 {
		empty := i18n.T(l, "assistant-feishu_cards.no_chats")
		return c.Markdown(empty), title + "\n\n" + empty
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	st, _ := s.feishuStatusKept(ctx)
	web := strings.TrimRight(st.PublicURL, "/") // every row's link is on this web app; "" keeps all buttons out
	for i, sess := range list {
		if i == 8 {
			break
		}
		name := clip(oneLine(conversationName(sess, main)), botListNameMost)
		line := "**" + feishubot.EscapeMarkdown(name) + "**"
		if !sess.UpdatedAt.IsZero() {
			line += " · " + botAgo(l, sess.UpdatedAt)
		}
		if sess.LastText != "" {
			line += "\n" + feishubot.EscapeMarkdown(clip(oneLine(sess.LastText), 80))
		}
		kind := targetPersona
		if sess.SessionID == main {
			kind = targetMain
		}
		switchBtn := feishubot.Btn(i18n.T(l, "assistant-feishu_cards.chat_here"), feishubot.Default, map[string]any{"a": "switch", "k": kind, "m": sess.MachineID, "s": sess.SessionID, "t": name})
		// The rows are Persona conversations (conversations lists Assistant sessions), so the link is /persona/<id>.
		openBtn := s.openLinkButton(ctx, sessionLink(web, sess.MachineID, sess.SessionID, sess.Assistant))
		c.Markdown(line).Buttons(switchBtn, openBtn)
		fmt.Fprintf(&b, "- %s\n", fallbackText.Replace(name))
	}
	return c.Note(i18n.T(l, "assistant-feishu_cards.new_hint")), strings.TrimSpace(b.String())
}

// botAgo is how long ago t was, in l (textfmt.Ago's wording in Chinese).
func botAgo(l i18n.Lang, t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return i18n.T(l, "assistant-feishu_cards.ago_now")
	case d < time.Hour:
		return i18n.T(l, "assistant-feishu_cards.ago_minutes", i18n.Args{"count": int(d.Minutes())})
	case d < 24*time.Hour:
		return i18n.T(l, "assistant-feishu_cards.ago_hours", i18n.Args{"count": int(d.Hours())})
	}
	return i18n.T(l, "assistant-feishu_cards.ago_days", i18n.Args{"count": int(d.Hours() / 24)})
}

// agentsCard lists the agents worth talking to: needing you, working, finished unseen, then the ones idle lately.
// It also returns the same list as plain text, used as the card's fallback when Feishu refuses the card.
func (s *Service) agentsCard(ctx context.Context, query string) (*feishubot.Card, string) {
	type row struct {
		d    Device
		sess proto.Session
	}
	var rows []row
	q := strings.ToLower(query)
	for _, d := range s.devices(ctx) {
		list, _ := s.sessions(ctx, d.ID)
		for _, sess := range list {
			if sess.Assistant || sess.State == proto.StateOffline || sess.State == proto.StateUnknown {
				continue
			}
			if sess.State == proto.StateIdle && time.Since(sess.UpdatedAt) > 48*time.Hour {
				continue
			}
			if q != "" && !strings.Contains(strings.ToLower(sess.Title+" "+sess.Cwd+" "+sess.SpaceName+" "+sess.SlotName+" "+d.Name), q) {
				continue
			}
			rows = append(rows, row{d, sess})
		}
	}
	order := map[string]int{proto.StateNeedsYou: 0, proto.StateWorking: 1, proto.StateUnread: 2, proto.StateIdle: 3}
	sort.SliceStable(rows, func(i, j int) bool {
		oi, oj := order[rows[i].sess.State], order[rows[j].sess.State]
		if oi != oj {
			return oi < oj
		}
		return rows[i].sess.UpdatedAt.After(rows[j].sess.UpdatedAt)
	})
	l := s.botLang(ctx)
	title := i18n.T(l, "assistant-feishu_cards.agents_title")
	c := feishubot.NewCard(title, feishubot.Purple, i18n.T(l, "assistant-feishu_cards.agents_summary"))
	startNew := feishubot.Btn(i18n.T(l, "assistant-feishu_cards.start_new"), feishubot.Primary, map[string]any{"a": "start_form"})
	if len(rows) == 0 {
		empty := i18n.T(l, "assistant-feishu_cards.no_agents")
		return c.Markdown(empty).Buttons(startNew), title + "\n\n" + empty
	}
	var b strings.Builder
	b.WriteString(title + "\n")
	icons := map[string]string{proto.StateNeedsYou: "🔴", proto.StateWorking: "🟢", proto.StateUnread: "🔵", proto.StateIdle: "⚪️"}
	for i, r := range rows {
		if i == 10 {
			more := i18n.T(l, "assistant-feishu_cards.more_agents", i18n.Args{"count": len(rows) - 10})
			c.Note(more)
			b.WriteString(more + "\n")
			break
		}
		label := clip(oneLine(agentLabel(r.sess)), botListNameMost)
		name := clip(oneLine(r.d.Name), botListNameMost)
		line := icons[r.sess.State] + " **" + feishubot.EscapeMarkdown(label) + "** · " + feishubot.EscapeMarkdown(name) + " · " + i18n.T(l, "assistant-feishu_cards.state_"+r.sess.State)
		if r.sess.LastText != "" {
			line += "\n" + feishubot.EscapeMarkdown(clip(oneLine(r.sess.LastText), 90))
		}
		c.Markdown(line).Buttons(feishubot.Btn(i18n.T(l, "assistant-feishu_cards.chat_here"), feishubot.Default,
			map[string]any{"a": "switch", "k": targetAgent, "m": r.d.ID, "s": r.sess.SessionID, "t": label + " · " + name}))
		fmt.Fprintf(&b, "- %s %s · %s\n", icons[r.sess.State], fallbackText.Replace(label), fallbackText.Replace(name))
	}
	return c.Buttons(startNew), strings.TrimSpace(b.String())
}

// startAgentCard is a form to start a coding agent on a machine.
func (s *Service) startAgentCard(ctx context.Context, prompt string) *feishubot.Card {
	var opts []feishubot.Option
	for _, d := range s.devices(ctx) {
		if d.Online {
			opts = append(opts, feishubot.Option{Label: d.Name, Value: d.ID})
		}
	}
	l := s.botLang(ctx)
	c := feishubot.NewCard(i18n.T(l, "assistant-feishu_cards.start_title"), feishubot.Purple, i18n.T(l, "assistant-feishu_cards.start_title"))
	if len(opts) == 0 {
		return c.Markdown(i18n.T(l, "assistant-feishu_cards.no_machines"))
	}
	fields := []any{
		feishubot.Select("machine", i18n.T(l, "assistant-feishu_cards.which_machine"), opts, false),
		feishubot.Input("cwd", i18n.T(l, "assistant-feishu_cards.directory"), i18n.T(l, "assistant-feishu_cards.directory_placeholder"), false),
		feishubot.Input("prompt", i18n.T(l, "assistant-feishu_cards.what_to_do"), i18n.T(l, "assistant-feishu_cards.what_to_do_placeholder"), true),
	}
	if prompt != "" {
		fields[2].(map[string]any)["default_value"] = prompt
	}
	if s.workbench.on.Load() { // the workbench's permission field, before what to do (feishu_workbench.go)
		fields = append(fields[:2], wbModeSelect(l), fields[2])
	}
	return c.Form("start", fields, feishubot.Submit(i18n.T(l, "assistant-feishu_cards.start"), feishubot.Primary, "start_submit", map[string]any{"a": "start"}))
}
