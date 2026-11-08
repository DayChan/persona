package hub

import (
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// withInjected marks, on items going to the apps, the user lines Persona wrote for the model (a task's notice, a
// scheduled run, a wake-up…): the apps show one line instead of a bubble with the model's instructions in it
// (docs/protocol/protocol.md "Messages Persona injects"). Read off the text, so old lines and older nodes' get it too; a line an app
// sent (client_msg_id) is the user's whatever it says. A task notice is tied to its task when the assistant's report
// names one open task by that title.
func (h *Hub) withInjected(items []proto.Item) []proto.Item {
	for i := range items {
		it := &items[i]
		if it.Role != "user" || it.Kind != proto.KindText || it.ClientMsgID != "" || it.From != "" || it.Injected != nil {
			continue
		}
		in := prompts.ParseInjected(it.Text)
		if in == nil {
			continue
		}
		if in.Kind == proto.InjectedTaskNotice {
			in.TodoID = h.taskByTitle(in.Title)
		}
		in.Line = injectedLine(h.userLang(), in)
		it.Injected = in
	}
	return items
}

// taskByTitle is the open task (a todo an assistant conversation opened) titled title; "" when none or several.
func (h *Hub) taskByTitle(title string) string {
	tl := &h.todoLinks
	tl.mu.RLock()
	defer tl.mu.RUnlock()
	id := ""
	for _, l := range tl.links {
		if !l.Task || l.Title != title {
			continue
		}
		if id != "" && id != l.TodoID {
			return ""
		}
		id = l.TodoID
	}
	return id
}

// injectedLine is the one line the apps show for an injected message, in l (docs/protocol/i18n.md). The message itself stays as
// the model read it (Chinese: prompts.ParseInjected reads it back); only the line is the user's language. zh-Hans is
// what ParseInjected wrote.
func injectedLine(l i18n.Lang, in *proto.Injected) string {
	if l == i18n.ZhHans {
		return in.Line
	}
	args := i18n.Args{"title": in.Title}
	switch in.Kind {
	case proto.InjectedTaskNotice:
		if in.State != "" {
			return i18n.T(l, "injected.task_"+in.State, args)
		}
	case proto.InjectedFollowup:
		switch in.State {
		case "wake":
			return i18n.T(l, "injected.wake", args)
		case "limit_continue":
			return i18n.T(l, "injected.limit_continue")
		case "once":
			return i18n.T(l, "injected.followup_due", args)
		case "daily", "interval":
			return i18n.T(l, "injected.routine_started", args)
		}
	case proto.InjectedSystem:
		if in.State == "inbox" {
			return i18n.T(l, "injected.inbox_update", args)
		}
		if in.State == "lost_tasks" {
			// the count is in the Chinese line: "node restarted, 3 background tasks stopped"
			n := 0
			for _, f := range strings.FieldsFunc(in.Line, func(r rune) bool { return r < '0' || r > '9' }) {
				for _, c := range f {
					n = n*10 + int(c-'0')
				}
				break
			}
			if n > 0 {
				return i18n.T(l, "injected.lost_tasks_n", i18n.Args{"count": n})
			}
			return i18n.T(l, "injected.lost_tasks")
		}
	}
	return in.Line
}
