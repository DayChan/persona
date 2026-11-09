package assistant

import (
	"encoding/json"
	"path"
	"strings"

	"github.com/nwverse/persona/server/internal/feishubot"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// botStep is one line of the progress card for a tool call, in l: the verb, then what it works on (code when it is a
// command, file or pattern) and a note.
func botStep(l i18n.Lang, it proto.Item) string {
	s := stepOf(l, it)
	line := s.verb
	switch {
	case s.target == "":
	case s.plain:
		line += " · " + feishubot.EscapeMarkdown(s.target)
	default:
		line += " " + feishubot.Code(s.target)
	}
	return withNote(line, feishubot.EscapeMarkdown(s.note))
}

// botStepParts is what a tool call did, in l: the verb ("" when it is no tool call) and what it did it to.
func botStepParts(l i18n.Lang, it proto.Item) (string, string) {
	s := stepOf(l, it)
	return s.verb, strings.TrimSpace(withNote(s.target, s.note))
}

// withNote puts a note after s: a space, none before a full-width bracket.
func withNote(s, note string) string {
	switch {
	case note == "":
		return s
	case strings.HasPrefix(note, "（"):
		return s + note
	}
	return s + " " + note
}

// step is a tool call said plainly: a verb, what it works on (plain: words, not code) and a short note after it.
type step struct {
	verb, target, note string
	plain              bool
}

// stepOf reads a tool call into a step. Codex's calls arrive mapped to Claude's tools (internal/transcript/codex.go).
func stepOf(l i18n.Lang, it proto.Item) step {
	if it.Kind != proto.KindToolUse {
		return step{}
	}
	var in map[string]any
	_ = json.Unmarshal(it.ToolInput, &in)
	str := func(k string) string { v, _ := in[k].(string); return strings.TrimSpace(v) }
	num := func(k string) int { v, _ := in[k].(float64); return int(v) }
	t := func(key string, a ...i18n.Args) string { return i18n.T(l, "assistant-feishu."+key, a...) }
	base := func(k string) string {
		if p := str(k); p != "" {
			return path.Base(p)
		}
		return ""
	}
	var s step
	switch name := it.ToolName; name {
	case "Bash":
		s.verb = t("step_run")
		if b, _ := in["run_in_background"].(bool); b {
			s.verb = t("step_run_background")
		}
		if d := str("description"); d != "" {
			s.target, s.plain = d, true
		} else {
			s.target = firstLine(str("command"))
		}
	case "Read":
		s.verb, s.target = t("step_read"), base("file_path")
		if from, n := num("offset"), num("limit"); from > 0 || n > 0 {
			from = max(from, 1)
			if n > 0 {
				s.note = t("step_lines", i18n.Args{"from": from, "to": from + n - 1})
			} else {
				s.note = t("step_lines_from", i18n.Args{"from": from})
			}
		}
	case "Edit", "MultiEdit", "NotebookEdit":
		s.verb, s.target = t("step_edit"), base("file_path")
		if s.target == "" {
			s.target = base("notebook_path")
		}
		switch str("change") { // Codex's file changes
		case "add":
			s.verb = t("step_create")
		case "delete":
			s.verb = t("step_delete")
		}
	case "Write":
		s.verb, s.target = t("step_write"), base("file_path")
	case "Grep", "Glob":
		s.verb, s.target = t("step_search"), str("pattern")
		where, glob := str("path"), ""
		if name == "Grep" {
			glob = str("glob")
		}
		switch {
		case where != "" && glob != "":
			s.note = t("step_where_glob", i18n.Args{"where": where, "glob": glob})
		case where != "":
			s.note = t("step_where", i18n.Args{"where": where})
		case glob != "":
			s.note = t("step_glob", i18n.Args{"glob": glob})
		}
	case "WebFetch":
		s.verb, s.target = t("step_open_web"), str("url")
	case "WebSearch":
		s.verb, s.target = t("step_search_web"), str("query")
	case "Task", "Agent":
		s.verb, s.target, s.plain = t("step_subtask"), str("description"), true
		if a := str("subagent_type"); a != "" {
			s.note = t("step_agent_type", i18n.Args{"type": a})
		}
	case "TodoWrite":
		s.verb, s.target, s.plain = t("step_todos"), stepNow(in), true
	case "Skill":
		s.verb, s.target = t("step_skill"), str("skill")
	default:
		tool := name
		if strings.HasPrefix(tool, "mcp__") {
			if i := strings.LastIndex(tool, "__"); i > 4 {
				tool = tool[i+2:]
			}
		}
		s.verb = tool // an unknown tool goes by its name
		key := stepMCP[tool]
		if key == "" && strings.HasPrefix(tool, "kb_") {
			key = "step_kb"
		}
		if key != "" {
			s.verb = t(key)
		}
		for _, k := range []string{"query", "q", "title", "to", "subject", "command", "cmd", "path", "file_path", "url", "name", "id"} {
			if v := str(k); v != "" {
				s.target = firstLine(v)
				break
			}
		}
	}
	s.target = clip(oneLine(s.target), 60)
	return s
}

// stepMCP is the verb for Persona's own MCP tools (internal/prompts/mcp), by the tool's name.
var stepMCP = map[string]string{
	"phone_calendar_events":       "step_calendar",
	"phone_calendar_create_event": "step_calendar_add",
	"phone_contacts_search":       "step_contacts",
	"phone_reminders_list":        "step_reminders_list",
	"phone_reminders_create":      "step_reminders_create",
	"phone_reminders_complete":    "step_reminders_complete",
	"phone_reminders_update":      "step_reminders_update",
	"phone_reminders_delete":      "step_reminders_delete",
	"phone_calendar_update_event": "step_calendar_update",
	"phone_calendar_delete_event": "step_calendar_delete",
	"phone_contacts_create":       "step_contacts_create",
	"phone_contacts_update":       "step_contacts_update",
	"phone_alarm_create":          "step_alarm_create",
	"phone_timer_start":           "step_timer_start",
	"phone_messages_compose":      "step_message_compose",
	"phone_call_start":            "step_call",
	"phone_maps_search_nearby":    "step_maps_nearby",
	"phone_maps_navigate":         "step_maps_navigate",
	"phone_shortcuts_run":         "step_shortcut_run",
	"phone_media_play":            "step_media_play",
	"phone_home_control":          "step_home_control",
	"phone_home_run_scene":        "step_home_scene",
	"mail_search":                 "step_mail_search",
	"mail_read":                   "step_mail_read",
	"mail_send":                   "step_mail_send",
	"kb_update":                   "step_kb_update",
	"search_knowledge":            "step_kb",
}

// stepNow is what a to-do list (TodoWrite's todos) is doing now: its in-progress item.
func stepNow(in map[string]any) string {
	list, _ := in["todos"].([]any)
	for _, x := range list {
		m, _ := x.(map[string]any)
		if st, _ := m["status"].(string); st != "in_progress" {
			continue
		}
		for _, k := range []string{"activeForm", "content"} {
			if v, _ := m[k].(string); strings.TrimSpace(v) != "" {
				return v
			}
		}
	}
	return ""
}
