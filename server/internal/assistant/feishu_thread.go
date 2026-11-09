package assistant

import (
	"context"
	"errors"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/channel"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// A message from a group chat's topic (#5279, docs/assistant/todos.md "Chat thread", docs/extensions/bots.md): the bot's chat with the
// owner is the private one; a topic is a place the owner talks about one to-do. The hub does not hand group messages over yet
// (its own gate comes with a later step), so this is also the second line when it does.
//
// A topic message is not the private chat's: no commands (a text starting with "/" is a word like any other), no
// answering the waiting card or a "Please verify" card, and never the chat's current target. It goes by the topic
// alone: the to-do bound to {feishu, chat_id, thread_id} (Todo.Thread) takes it as a decision and its agent gets it,
// and the answer goes back into the same topic. A topic bound to nothing open gets one short hint.

// feishuTopic is the chat and thread a message was said in.
type feishuTopic struct{ Chat, Thread string }

// target is where what is sent for that topic goes: its chat, as a reply into the thread. The zero topic (nil) is the
// private chat, where nothing changes.
func (t *feishuTopic) target() feishuTarget {
	if t == nil {
		return feishuTarget{}
	}
	return feishuTarget{ChatID: t.Chat, InThread: true}
}

// runTo is where a run's messages go, from its channel values (Ext): the topic it was asked in, else the private chat.
func runTo(ext map[string]string) feishuTarget {
	if ext["chat"] == "" || ext["thread"] == "" {
		return feishuTarget{}
	}
	return (&feishuTopic{Chat: ext["chat"], Thread: ext["thread"]}).target()
}

// threadHintEvery is how often a topic is told again that it is tied to no open to-do.
const threadHintEvery = 10 * time.Minute

// threadHintsKeep bounds the topics remembered for it.
const threadHintsKeep = 200

// threadMessage takes a message from a group chat: the to-do bound to its topic records it and its agent gets it.
func (s *Service) threadMessage(ctx context.Context, ev feishubot.MessageEvent, st FeishuStatus, content feishubot.Content) {
	msg := ev.Message
	// Only the owner's own words count here, whatever reaches this far: the hub checks it too (a later step), and the
	// words go to an agent that acts on them.
	if ev.Sender.SenderType != "user" || st.Owner == "" || ev.Sender.SenderID.OpenID != st.Owner {
		s.logf("bots: a group message not from the owner was dropped")
		return
	}
	if msg.ChatID == "" || msg.ThreadID == "" {
		s.logf("bots: a message outside a topic of a group chat was left alone")
		return // nowhere to answer in, and not a topic of a to-do
	}
	// Where the bot's own lines into this topic reply to (a todo's posts, feishu_thread_posts.go): any message of it will do.
	s.rememberTopicMessage(ctx, msg.ChatID, msg.ThreadID, msg.MessageID)
	l := s.botLang(ctx)
	topic := &feishuTopic{Chat: msg.ChatID, Thread: msg.ThreadID}
	to := topic.target()
	if err := s.ensureTodosLoaded(ctx); err != nil {
		s.logf("bots: topic message: todos not loaded: %v", err)
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu.not_sent", i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
		return
	}
	t, bound := s.todoOfThread(TodoThread{Channel: channel.Feishu, ChatID: msg.ChatID, ThreadID: msg.ThreadID})
	if !bound && s.openTopicIfAsked(ctx, ev, st, content, topic) {
		return // a topic bound to nothing that starts one, or "/ai …" in one: a to-do is opened (feishu_thread_open.go)
	}
	if bound && !content.Forward && noReplyCommand(openingWords(ev, content)) { // after the bot's @, which a group that only sends what @s the bot starts every message with
		// "Don't report back": a command of its own, also for a todo that is done and not yet reported, and never passed on
		// as words (todo_source.go).
		s.threadNoReply(ctx, ev, t, topic)
		return
	}
	if !bound || t.DoneAt != nil {
		key := "assistant-feishu.thread_unbound"
		if bound {
			key = "assistant-feishu.thread_done"
		}
		// "/ai" for a to-do that is done is asked for in so many words: it is answered each time, not once in ten minutes.
		if _, asked := aiCommand(openingWords(ev, content)); bound && asked || s.threadHintDue(msg.ChatID+"/"+msg.ThreadID) {
			s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, key))
		}
		return
	}
	text := strings.TrimSpace(content.Text)
	if content.Forward || text == "" && len(content.Media) == 0 {
		// Merged forwards are not opened here (others' words), and a message with nothing in it has nothing to pass on.
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu.cannot_read"))
		return
	}
	// Who gets it is settled before anything is written; with no one, the words are still recorded and the user is told so.
	routes, routeErr := s.threadRoutes(ctx, t, st.Features)
	recorded := false
	if text != "" {
		if store := s.todoDB(); store != nil {
			at := decisionNow()
			inserted, err := s.insertDecision(ctx, store, db.TodoDecisionRow{ID: "dec_" + randomHex(8), TodoID: t.ID,
				Question: i18n.T(l, "assistant-feishu.thread_question"), Choice: decisionText(text), Source: db.DecisionThread,
				SourceMessage: "feishu-" + msg.MessageID, CreatedAt: at})
			switch {
			case errors.Is(err, db.ErrTodoNotFound): // deleted since it was looked up
				if s.threadHintDue(msg.ChatID + "/" + msg.ThreadID) {
					s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, "assistant-feishu.thread_unbound"))
				}
				return
			case err != nil:
				s.logf("todo %s: topic decision: %v", t.ID, err) // as an acceptance's: the words still go to the agent
			case !inserted:
				return // this message was handed over before: once is all
			default:
				recorded = true
				s.decisionsChanged(ctx, t.ID, at)
			}
		}
	}
	notDelivered := func(err error) {
		key := "assistant-feishu.not_sent"
		if recorded {
			key = "assistant-feishu.thread_recorded_not_sent"
		}
		s.botNoteTo(ctx, to, msg.MessageID, i18n.T(l, key, i18n.Args{"error": clip(oneLine(err.Error()), 200)}))
	}
	if len(routes) == 0 {
		s.logf("bots: topic message of todo %s: no one to give it to: %v", t.ID, routeErr)
		notDelivered(routeErr)
		return
	}
	content.Text = prompts.RenderTodoThreadReply(prompts.TodoThreadReply{Title: contextText(clip(oneLine(t.Title), 80)), Text: text, Recorded: recorded})
	// A to-do that came from a question nobody answered (ask_todo.go): the reply is the owner's call, and goes to the
	// conversation that asked (not to an Agent) as the answer; the to-do then closes.
	asked := false
	if convs := slices.DeleteFunc(slices.Clone(routes), func(r botTarget) bool { return r.Kind == targetAgent }); text != "" && len(convs) > 0 {
		if answer, ok := s.askTodoTopicAnswer(ctx, t, text); ok {
			content.Text, routes, asked = answer, convs, true
		}
	}
	var err error
	for _, target := range routes { // one that cannot take it (offline, gone, refusing) leaves it to the next
		if err = s.botSendIn(ctx, &ev, content, target, topic); err == nil {
			if asked {
				s.settleAskClaim(ctx, t.ID, true)
				s.closeAskTodo(ctx, t.ID)
			}
			return
		}
		s.logf("bots: topic message of todo %s to %s: %v", t.ID, target.Kind, err)
	}
	if asked {
		s.settleAskClaim(ctx, t.ID, false)
	}
	notDelivered(err)
}

// todoOfThread is the todo th is bound to (open or done), from memory (the todos are loaded).
func (s *Service) todoOfThread(th TodoThread) (Todo, bool) {
	s.todos.mu.RLock()
	defer s.todos.mu.RUnlock()
	for _, t := range s.todos.todos {
		if t.Thread != nil && sameThread(*t.Thread, th) {
			return copyTodoLocked(t), true
		}
	}
	return Todo{}, false
}

// threadHintDue says whether the topic key is to be told it has no open to-do now: once in threadHintEvery.
func (s *Service) threadHintDue(key string) bool {
	b := &s.fbot
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock()
	if at, ok := b.hinted[key]; ok && now.Sub(at) < threadHintEvery {
		return false
	}
	if b.hinted == nil {
		b.hinted = map[string]time.Time{}
	}
	for k, at := range b.hinted {
		if now.Sub(at) >= threadHintEvery {
			delete(b.hinted, k)
		}
	}
	if len(b.hinted) >= threadHintsKeep { // a flood of topics: the hints stop rather than the table grow
		return false
	}
	b.hinted[key] = now
	return true
}

// threadRoutes are who can get what was said in the topic of open todo t, best first: the agent it has that is running
// (the one that most needs it, pickTaskAgent; a closed one, offline or interrupted, is not counted), then the
// conversation that opened it (for a question's to-do, which has none, the conversation that asked: its record,
// ask_todo.go), then the main conversation. One the bot's switches do not allow, or that is not there,
// is left out; the error is why none is left (nil when there are routes).
func (s *Service) threadRoutes(ctx context.Context, t Todo, f FeishuFeatures) ([]botTarget, error) {
	var tried []botTarget
	var running []proto.Session
	for _, a := range s.taskAgents(ctx, t) {
		if a.Status != proto.StatusOffline && a.Status != proto.StatusInterrupted {
			running = append(running, a)
		}
	}
	if len(running) > 0 {
		c := pickTaskAgent(running)
		tried = append(tried, botTarget{Kind: targetAgent, MachineID: c.MachineID, SessionID: c.SessionID})
	}
	if t.Origin != "" {
		tried = append(tried, botTarget{Kind: targetPersona, SessionID: t.Origin})
	} else if a, ok := s.askTodoOf(ctx, t.ID); ok {
		tried = append(tried, botTarget{Kind: targetPersona, SessionID: a.SessionID})
	}
	tried = append(tried, botTarget{Kind: targetMain})
	l := s.botLang(ctx)
	var out []botTarget
	var last error
	for _, c := range tried {
		if err := botAllowed(l, f, c); err != nil {
			last = err
			continue
		}
		if c.Kind == targetPersona {
			// The conversation is looked for: a todo outlives the conversation that opened it.
			if _, ok := s.session(ctx, s.settings(ctx).MachineID, c.SessionID); !ok {
				continue
			}
		}
		if _, err := s.resolveBotTarget(ctx, c); err != nil {
			last = err
			continue
		}
		out = append(out, c)
	}
	if len(out) == 0 && last == nil {
		last = errors.New(i18n.T(l, "assistant-feishu.no_assistant_machine"))
	}
	return out, last
}
