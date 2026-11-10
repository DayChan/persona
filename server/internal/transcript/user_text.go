package transcript

import (
	"crypto/sha256"
	"encoding/json"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

func (m *Meta) applyQueue(op, content string) {
	// Queued text keeps the <pasted_content> wrapper of long / voice input; show what the user wrote.
	content = strings.TrimSpace(dropTagged(pastedRe, "pasted_content", content))
	if from, body, ok := CrossSession(content); ok {
		content = "【来自 " + from + "】" + body
	}
	removeOne := func() bool {
		for i, q := range m.Queue {
			if q == content {
				m.Queue = append(m.Queue[:i:i], m.Queue[i+1:]...)
				return true
			}
		}
		return false
	}
	switch op {
	case "enqueue":
		if content != "" {
			m.Queue = append(m.Queue, content)
		}
	case "dequeue":
		if len(m.Queue) > 0 {
			m.Queue = m.Queue[1:]
		}
	case "remove", "popAll":
		if content == "" || !removeOne() {
			if op == "popAll" {
				m.Queue = nil
			} else if len(m.Queue) > 0 {
				m.Queue = m.Queue[1:]
			}
		}
	}
}

// caveatRe drops Claude Code's note that the lines around it came from a local command (context for the model).
var caveatRe = lazyre.New(`(?s)<local-command-caveat>.*?</local-command-caveat>`)

var teammateRe = lazyre.New(`(?s)<teammate-message([^>]*)>\s*(.*?)\s*</teammate-message>`)
var teammateIDRe = lazyre.New(`teammate_id="([^"]*)"`)

// dropTagged removes re's matches from s, every one of which holds lit. A replacement copies s even when nothing
// matches, and every user line comes through here: a line without lit is given back as it is.
func dropTagged(re *lazyre.Regexp, lit, s string) string {
	if !strings.Contains(s, lit) {
		return s
	}
	return re.ReplaceAllString(s, "")
}

// noticeKey is what Meta.notified keeps of a notification: a digest, not the text, which can be kilobytes.
type noticeKey [16]byte

func noticeKeyOf(s string) noticeKey {
	sum := sha256.Sum256([]byte(s))
	return noticeKey(sum[:16])
}

func userText(mk func(int, string, string) proto.Item, s string, meta *Meta) []proto.Item {
	s = dropTagged(caveatRe, "<local-command-caveat>", dropTagged(pastedRe, "pasted_content", dropTagged(ContextRe, "-context>", s)))
	trimmed := legacyLarkAgent(strings.TrimSpace(s))
	if trimmed == "" {
		return nil
	}
	if from, body, ok := CrossSession(trimmed); ok {
		it := mk(0, "user", proto.KindText)
		it.Text, it.From = clip(body), from
		return []proto.Item{it}
	}
	if m := teammateRe.FindStringSubmatch(trimmed); m != nil { // agent teams: a teammate reporting to the lead
		from := "队友"
		if n := teammateIDRe.FindStringSubmatch(m[1]); n != nil && n[1] != "" {
			from = n[1]
		}
		it := mk(0, "user", proto.KindText)
		it.Text, it.From = clip(teammateNote(strings.TrimSpace(m[2]))), from
		return []proto.Item{it}
	}
	// "! cmd" typed in Claude Code runs the command directly: the input reads as the user's own line, its output
	// as a Bash call under it (the input line comes first; its command is kept for the output's call).
	if strings.HasPrefix(trimmed, "<bash-input>") {
		cmd := strings.TrimSpace(firstTag(trimmed, "bash-input"))
		meta.bashInput, meta.localInput = strings.Clone(cmd), true // kept until its output: not the whole line
		it := mk(0, "user", proto.KindText)
		it.Text = clip("! " + cmd)
		return []proto.Item{it}
	}
	if strings.HasPrefix(trimmed, "<bash-stdout>") || strings.HasPrefix(trimmed, "<bash-stderr>") {
		out := strings.TrimSpace(strings.TrimSpace(firstTag(trimmed, "bash-stdout")) + "\n" + strings.TrimSpace(firstTag(trimmed, "bash-stderr")))
		input, _ := json.Marshal(map[string]string{"command": meta.bashInput})
		meta.bashInput = ""
		meta.endLocalTurn()
		use := mk(0, "assistant", proto.KindToolUse)
		id := strings.TrimSuffix(use.ID, "#0") + "-bash"
		use.ToolUseID, use.ToolName, use.ToolInput = id, "Bash", input
		res := mk(1, "user", proto.KindToolResult)
		res.ToolUseID, res.Text, res.IsError = id, clip(out), strings.TrimSpace(firstTag(trimmed, "bash-stderr")) != ""
		res.Outcome = &proto.ToolOutcome{Status: proto.OutcomeOK, Tail: outputTail(out)} // no exit code is recorded
		if res.IsError {
			res.Outcome.Status = proto.OutcomeFailed
		}
		return []proto.Item{use, res}
	}
	if strings.HasPrefix(trimmed, "<command-name>") || strings.HasPrefix(trimmed, "<command-message>") {
		name := firstTag(trimmed, "command-name")
		args := firstTag(trimmed, "command-args")
		if name == "/effort" && isUltracodeCommand(args) && !meta.tree.sidechain {
			// The command's confirmed output becomes one notice; the command itself is not a user prompt.
			meta.localInput = true
			return nil
		}
		if name == "/compact" && strings.TrimSpace(args) == "" {
			// Persona compacts an idle assistant conversation itself (node/assistant_reap.go): a notice, not a message
			// the user sent, and not activity that would make the conversation unread.
			it := mk(0, "system", proto.KindNotice)
			it.Text = CompactNotice
			return []proto.Item{it}
		}
		meta.localInput = true
		it := mk(0, "user", proto.KindText)
		it.Text = strings.TrimSpace(name + " " + args)
		return []proto.Item{it}
	}
	if strings.HasPrefix(trimmed, "<local-command-stdout>") || strings.HasPrefix(trimmed, "<local-command-stderr>") {
		meta.endLocalTurn()
		it := mk(0, "system", proto.KindNotice)
		it.Text = clip(strings.TrimSpace(ansiRe.ReplaceAllString(tagRe.ReplaceAllString(trimmed, "$2"), "")))
		if it.Text == "" {
			return nil
		}
		meta.claudeGoalOutput(it.Text, meta.Updated)
		if strings.HasPrefix(trimmed, "<local-command-stdout>") && !meta.tree.sidechain {
			if strings.HasPrefix(it.Text, "Set model to ") {
				meta.clearModelFallback()
			}
			if items, ok := meta.claudeUltracodeOutput(it.Text, it.TS, it); ok {
				return items
			}
		}
		return []proto.Item{it}
	}
	if strings.HasPrefix(trimmed, "<task-notification>") {
		key := noticeKeyOf(trimmed)
		if meta.notified[key] {
			return nil
		}
		if meta.notified == nil {
			meta.notified = map[noticeKey]bool{}
		}
		meta.notified[key] = true
		it := mk(0, "system", proto.KindNotice)
		it.Task, it.Text = taskNote(trimmed)
		meta.noteCall(it.Task)
		meta.reportFor(it.Task)
		return []proto.Item{it}
	}
	if trimmed == "[Request interrupted by user]" || trimmed == "[Request interrupted by user for tool use]" {
		meta.TurnOpen, meta.TurnClosed = false, true
		it := mk(0, "system", proto.KindNotice)
		it.Text = InterruptedNotice
		return []proto.Item{it}
	}
	text := strings.TrimSpace(tagRe.ReplaceAllStringFunc(trimmed, func(m string) string {
		if strings.HasPrefix(m, "<system-reminder>") {
			return ""
		}
		return m
	}))
	if text == "" {
		return nil
	}
	it := mk(0, "user", proto.KindText)
	it.Text = clip(text)
	return []proto.Item{it}
}

// endLocalTurn: a local command's output came, so the turn it started (no agent reply in between) is over and the
// turn state is what it was before the command: Claude Code writes no turn_duration for it (#4661).
func (m *Meta) endLocalTurn() {
	if m.localTurn && m.TurnOpen {
		m.TurnOpen, m.TurnClosed = m.beforeLocal[0], m.beforeLocal[1]
	}
	m.localTurn = false
}
