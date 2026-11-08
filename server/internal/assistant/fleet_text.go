package assistant

import (
	"path/filepath"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// The node writes a session's activity, a background command's stand-in and a queued message's sender in Chinese
// (internal/transcript): it does not know the user's language. The session lists the apps get have them in the user's
// language (docs/protocol/i18n.md); zh-Hans, and anything not recognized (an older or newer node's wording), go out as the node
// wrote them. The transcripts' own lines the hub rewrites for each app (hub/transcript_text.go).

const transcriptNS = "hub-transcript_text."

func tt(l i18n.Lang, key string, args ...i18n.Args) string {
	return i18n.T(l, transcriptNS+key, args...)
}

// fromName is the sender's name in l when it is the node's stand-in for a sender without one.
func fromName(l i18n.Lang, from string) string {
	switch from {
	case transcript.FromSession:
		return tt(l, "from_session")
	case "队友":
		return tt(l, "from_teammate")
	}
	return from
}

// ---- a session's activity, background tasks and queue ----

// activityLen is transcript's: an activity is one short line on a task card.
const activityLen = 40

// activityText is transcript.Activity's line in l; one it does not know stays as it is.
func activityText(l i18n.Lang, s string) string {
	if key, _ := transcript.ActivityKey(s); key != "" {
		return tt(l, key)
	}
	verb, what, ok := strings.Cut(s, " ")
	if !ok || what == "" {
		return s
	}
	key, isVerb := transcript.ActivityKey(verb)
	if !isVerb {
		return s
	}
	out := tt(l, key+"_what", i18n.Args{"what": what})
	if r := []rune(out); len(r) > activityLen {
		out = strings.TrimSuffix(string(r[:activityLen-1]), "…") + "…"
	}
	return out
}

// queuedFrom starts a queued message another session sent (transcript/user_text.go applyQueue).
const queuedFrom = "【来自 "

// localizeSession rewrites s's node-written lines in l: its activity, a background command's stand-in description, the
// sender of a queued message, what its workflow agents' and subagents' latest tool calls do. The node's slices are
// shared: changed ones are copied.
func localizeSession(l i18n.Lang, s *proto.Session) {
	if l == i18n.ZhHans || !i18n.Valid(l) {
		return
	}
	s.Activity = activityText(l, s.Activity)
	localizeWorkflows(l, s)
	localizeSubagents(l, s)
	s.RuntimeFallback = runtimeFallbackText(l, s.RuntimeFallback)
	s.ReadOnlyReason = claudeLiveText(l, s.ReadOnlyReason)
	s.AttentionReason = claudeLiveText(l, s.AttentionReason)
	copied := false
	for i, b := range s.Background {
		if b.Description == "超时转到后台的命令" {
			if !copied {
				s.Background, copied = append([]proto.BackgroundTask(nil), s.Background...), true
			}
			s.Background[i].Description = tt(l, "background_moved")
		}
	}
	copied = false
	for i, q := range s.Queued {
		rest, ok := strings.CutPrefix(q, queuedFrom)
		if !ok {
			continue
		}
		from, body, ok := strings.Cut(rest, "】")
		if !ok {
			continue
		}
		if !copied {
			s.Queued, copied = append([]string(nil), s.Queued...), true
		}
		s.Queued[i] = tt(l, "queued_from", i18n.Args{"from": fromName(l, from)}) + body
	}
}

// localizeWorkflows rewrites the summary of each workflow agent's latest tool call (transcript.Activity's line, as a
// session's activity) in l, copying the workflows, agents and tools it changes.
func localizeWorkflows(l i18n.Lang, s *proto.Session) {
	copied := false
	for i := range s.Workflows {
		var agents []proto.WorkflowAgent
		for j, a := range s.Workflows[i].Agents {
			if a.LatestTool == nil {
				continue
			}
			sum := activityText(l, a.LatestTool.Summary)
			if sum == a.LatestTool.Summary {
				continue
			}
			if agents == nil {
				agents = append([]proto.WorkflowAgent(nil), s.Workflows[i].Agents...)
			}
			agents[j].LatestTool = localizedTool(a.LatestTool, sum)
		}
		if agents == nil {
			continue
		}
		if !copied {
			s.Workflows, copied = append([]proto.Workflow(nil), s.Workflows...), true
		}
		s.Workflows[i].Agents = agents
	}
}

// localizeSubagents rewrites the summary of each subagent's latest tool call (the same activity line, #5602) in l,
// copying the subagents and tools it changes.
func localizeSubagents(l i18n.Lang, s *proto.Session) {
	copied := false
	for i, a := range s.Subagents {
		if a.LastTool == nil {
			continue
		}
		sum := activityText(l, a.LastTool.Summary)
		if sum == a.LastTool.Summary {
			continue
		}
		if !copied {
			s.Subagents, copied = append([]proto.Subagent(nil), s.Subagents...), true
		}
		s.Subagents[i].LastTool = localizedTool(a.LastTool, sum)
	}
}

// localizedTool is a copy of t (its call id and name kept) with summary for its summary.
func localizedTool(t *proto.WorkflowTool, summary string) *proto.WorkflowTool {
	c := *t
	c.Summary = summary
	return &c
}

// workLogLine is what the knowledge base's project timeline gets for a finished turn: its conclusion when the Agents
// business found it done, else its last words (kbAgentDone).
func workLogLine(sess proto.Session) string {
	if w := sess.Work; w != nil && w.Phase == proto.WorkDone && w.Line != "" {
		line := "完成：" + w.Line
		for _, a := range w.Artifacts {
			if a.Kind == "pr" || a.Kind == "doc" {
				line += " " + filepath.ToSlash(a.Ref)
			}
		}
		return line
	}
	return sess.LastText
}

// claudeLiveTexts are what the node says about a Claude session outside Persona's terminals (proto/claudelive.go),
// with their keys.
var claudeLiveTexts = map[string]string{
	proto.ReadOnlyClaudeBg:        "read_only_claude_bg",
	proto.ReadOnlyClaudeNoInbox:   "read_only_claude_no_inbox",
	proto.ReadOnlyClaudeInbox:     "read_only_claude_inbox",
	proto.AttentionClaudeComputer: "attention_claude_computer",
}

// claudeLiveText is one of them in l; anything else stays as it is.
func claudeLiveText(l i18n.Lang, s string) string {
	if key, ok := claudeLiveTexts[s]; ok {
		return tt(l, key)
	}
	return s
}

// runtimeFallbacks are the node's reasons for running an Agent SDK conversation on the CLI (proto.Fallback*) and their
// keys; the cause after one that takes it stays as it is.
var runtimeFallbacks = []struct {
	text, key string
	cause     bool
}{
	{proto.FallbackReadBridge, "fallback_read_bridge", true},
	{proto.FallbackNoBridge, "fallback_no_bridge", false},
	{proto.FallbackWriteBridge, "fallback_write_bridge", true},
	{proto.FallbackNoBridgeScript, "fallback_no_bridge_script", false},
	{proto.FallbackNoNode, "fallback_no_node", true},
	{proto.FallbackNoClaude, "fallback_no_claude", true},
}

// shellLauncherHead, shellLauncherTail: proto.FallbackShellLauncher around the launcher's name.
var shellLauncherHead, shellLauncherTail, _ = strings.Cut(proto.FallbackShellLauncher, "%s")

// runtimeFallbackText is the node's RuntimeFallback in l; one it does not know stays as it is.
func runtimeFallbackText(l i18n.Lang, s string) string {
	if s == "" {
		return s
	}
	for _, f := range runtimeFallbacks {
		if cause, ok := strings.CutPrefix(s, f.text); ok && (cause == "" || f.cause) {
			return tt(l, f.key, i18n.Args{"cause": cause})
		}
	}
	if rest, ok := strings.CutPrefix(s, shellLauncherHead); ok {
		if name, ok := strings.CutSuffix(rest, shellLauncherTail); ok {
			return tt(l, "fallback_shell_launcher", i18n.Args{"launcher": name})
		}
	}
	return s
}
