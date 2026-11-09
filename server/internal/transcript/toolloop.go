package transcript

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// LoopDetector consumes complete transcript lines. It retains only hashes and bounded
// metadata, never tool arguments or results. The caller owns one detector per file.
type LoopDetector struct {
	pending   []loopCall
	history   []loopCall
	candidate *proto.ToolLoop
	period    int
}

type loopCall struct {
	id, tool       string
	input, result  [32]byte
	at, resultAt   time.Time
	ready, polling bool
}

// Clone makes an independent copy for an incremental file read.
func (d LoopDetector) Clone() LoopDetector {
	d.pending = append([]loopCall(nil), d.pending...)
	d.history = append([]loopCall(nil), d.history...)
	if d.candidate != nil {
		c := *d.candidate
		d.candidate = &c
	}
	return d
}

// Candidate returns the current repetition summary.
func (d *LoopDetector) Candidate() *proto.ToolLoop {
	if d.candidate == nil {
		return nil
	}
	c := *d.candidate
	return &c
}

func (d *LoopDetector) reset() { *d = LoopDetector{} }

func loopHash(raw json.RawMessage) [32]byte {
	var value any
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if decoder.Decode(&value) == nil {
		if normalized, err := json.Marshal(value); err == nil {
			return sha256.Sum256(normalized)
		}
	}
	return sha256.Sum256(raw)
}

// Line reads message and response-item transcripts, avoiding duplicate
// item_completed representations of the same calls.
func (d *LoopDetector) Line(line []byte) {
	var l struct {
		Type, Subtype                         string
		Timestamp                             time.Time
		IsSidechain, IsMeta, IsCompactSummary bool
		Message                               *struct {
			Role    string
			Content json.RawMessage
		}
		Attachment *struct {
			Type, CommandMode string
			Origin            struct{ Kind string }
		}
		Payload json.RawMessage
	}
	if json.Unmarshal(line, &l) != nil || l.IsSidechain {
		return
	}
	switch l.Type {
	case "system":
		if l.Subtype == "turn_duration" {
			d.reset()
		}
	case "attachment":
		if l.Attachment != nil && l.Attachment.Type == "queued_command" && l.Attachment.CommandMode == "prompt" && l.Attachment.Origin.Kind == "human" {
			d.reset()
		}
	case "user", "assistant":
		if l.Message == nil || l.IsMeta || l.IsCompactSummary {
			return
		}
		var rawBlocks []json.RawMessage
		if json.Unmarshal(l.Message.Content, &rawBlocks) != nil {
			if l.Type == "user" {
				d.reset()
			}
			return
		}
		hasResult, hasPrompt := false, false
		for _, raw := range rawBlocks {
			var b struct {
				Type      string          `json:"type"`
				ID        string          `json:"id"`
				Name      string          `json:"name"`
				ToolUseID string          `json:"tool_use_id"`
				Input     json.RawMessage `json:"input"`
				Content   json.RawMessage `json:"content"`
				IsError   bool            `json:"is_error"`
			}
			if json.Unmarshal(raw, &b) != nil {
				continue
			}
			switch b.Type {
			case "text", "image":
				hasPrompt = true
			case "tool_use":
				d.call(b.ID, b.Name, b.Input, l.Timestamp)
			case "tool_result":
				hasResult = true
				r, _ := json.Marshal(struct {
					Content json.RawMessage `json:"content"`
					Error   bool            `json:"error"`
				}{b.Content, b.IsError})
				d.result(b.ToolUseID, r, l.Timestamp)
			}
		}
		if l.Type == "user" && (!hasResult || hasPrompt) {
			d.reset()
		}
	case "event_msg":
		var p struct {
			Type string
			Item *struct{ Type string }
		}
		if json.Unmarshal(l.Payload, &p) == nil && (p.Type == "task_started" || p.Type == "task_complete" || p.Type == "turn_aborted" || p.Type == "user_message" || p.Type == "item_completed" && p.Item != nil && p.Item.Type == "UserMessage") {
			d.reset()
		}
	case "response_item":
		var p struct {
			Type, Name               string
			Arguments, Input, Output json.RawMessage
			CallID                   string `json:"call_id"`
		}
		if json.Unmarshal(l.Payload, &p) != nil {
			return
		}
		switch p.Type {
		case "function_call", "custom_tool_call":
			raw := p.Arguments
			if p.Type == "custom_tool_call" {
				raw = p.Input
			}
			var in string
			if json.Unmarshal(raw, &in) == nil && json.Valid([]byte(in)) {
				raw = json.RawMessage(in)
			}
			d.call(p.CallID, p.Name, raw, l.Timestamp)
		case "function_call_output", "custom_tool_call_output":
			d.result(p.CallID, p.Output, l.Timestamp)
		}
	}
}

func safeToolName(name string) bool {
	if len(name) == 0 || len(name) > 80 {
		return false
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-' || r == '.') {
			return false
		}
	}
	return true
}

var (
	loopWaitCommand = lazyre.New(`(?m)(?:^|[;&|()])[\t ]*(?:do[\t ]+)?(?:sleep|wait)(?:[\t ]|$)`)
	loopTailCommand = lazyre.New(`(?m)(?:^|[;&|()])[\t ]*(?:do[\t ]+)?(?:sudo[\t ]+)?tail(?:[\t ]|$)`)
	loopLogCommand  = lazyre.New(`(?m)(?:^|[;&|()])[\t ]*(?:do[\t ]+)?(?:journalctl|docker[\t ]+logs|kubectl[\t ]+logs)(?:[\t ]|$)`)
	loopCICommand   = lazyre.New(`(?m)(?:^|[;&|()])[\t ]*(?:do[\t ]+)?gh[\t ]+(?:pr[\t ]+checks|run[\t ]+(?:watch|view|list))(?:[\t ]|$)`)
	loopCIView      = lazyre.New(`(?m)(?:^|[;&|()])[\t ]*(?:do[\t ]+)?gh[\t ]+pr[\t ]+view(?:[\t ]|$)`)
	loopCIApi       = lazyre.New(`(?m)(?:^|[;&|()])[\t ]*(?:do[\t ]+)?gh[\t ]+api(?:[\t ]|$)`)
	loopCIWrite     = lazyre.New(`(?:^|[\t ])(?:-x[\t ]*(?:post|put|patch|delete)|--method(?:=|[\t ]+)(?:post|put|patch|delete)|-f(?:[\t ]|=)|--(?:field|raw-field|input)(?:=|[\t ]))`)
)

func pollingTool(name string, input json.RawMessage) bool {
	n := strings.ToLower(name)
	if i := strings.LastIndex(n, "__"); i >= 0 {
		n = n[i+2:]
	}
	switch n {
	case "sleep", "wait", "monitor", "schedulewakeup", "bashoutput", "taskoutput", "get_ci_status", "get_check_runs", "get_check_suites", "get_pipeline_status":
		return true
	case "write_stdin", "functions.write_stdin":
		var in struct {
			SessionID json.RawMessage `json:"session_id"`
			Chars     string          `json:"chars"`
		}
		return json.Unmarshal(input, &in) == nil && len(in.SessionID) > 0 && string(in.SessionID) != "null" && in.Chars == ""
	case "read":
		var in struct {
			FilePath string `json:"file_path"`
			Offset   int    `json:"offset"`
			Limit    int    `json:"limit"`
		}
		if json.Unmarshal(input, &in) != nil || in.Offset <= 0 || in.Limit <= 0 {
			return false
		}
		path := strings.ToLower(in.FilePath)
		return strings.HasSuffix(path, ".log") || strings.Contains(path, ".log.") || strings.HasSuffix(path, "/syslog")
	case "bash", "functions.bash", "shell", "exec_command":
	default:
		return false
	}
	var in struct{ Command, Cmd string }
	var command string
	if json.Unmarshal(input, &in) == nil {
		command = in.Command
		if command == "" {
			command = in.Cmd
		}
	} else {
		_ = json.Unmarshal(input, &command)
	}
	return pollingCommand(strings.ToLower(command))
}

// Only exempt a compound command when every part is recognizably observation
// or waiting. Shell expansion and redirection make that impossible to tell
// without evaluating the command, so leave those calls eligible for triage.
func pollingCommand(command string) bool {
	parts, ok := pollingCommandParts(command)
	if !ok {
		return false
	}
	observed := false
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" || part == "while true" || part == "done" {
			continue
		}
		starts := func(re *lazyre.Regexp) bool {
			m := re.FindStringIndex(part)
			return m != nil && m[0] == 0
		}
		if starts(loopWaitCommand) || starts(loopLogCommand) || starts(loopCICommand) ||
			starts(loopTailCommand) && (strings.Contains(part, ".log") || strings.Contains(part, "/log/") || strings.Contains(part, "/logs/") || strings.Contains(part, "syslog")) ||
			starts(loopCIView) && strings.Contains(part, "statuscheckrollup") ||
			starts(loopCIApi) && !loopCIWrite.MatchString(part) && (strings.Contains(part, "check-runs") || strings.Contains(part, "check-suites") || strings.Contains(part, "/actions/runs")) {
			observed = true
			continue
		}
		return false
	}
	return observed
}

// Split only shell syntax we can prove has no expansion or redirection. Quoted
// literal arguments are safe; unknown constructs stay eligible for triage.
func pollingCommandParts(command string) ([]string, bool) {
	var parts []string
	var part strings.Builder
	var quote rune
	for _, r := range command {
		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			if r == '\n' || quote == '"' && strings.ContainsRune("$`\\\\", r) {
				return nil, false
			}
			part.WriteRune(r)
			continue
		}
		switch r {
		case '\'', '"':
			quote = r
		case ';', '&', '|', '\n':
			parts = append(parts, part.String())
			part.Reset()
		case '$', '`', '\\', '<', '>', '(', ')', '{', '}', '#':
			return nil, false
		default:
			part.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, false
	}
	return append(parts, part.String()), true
}

func (d *LoopDetector) call(id, name string, input json.RawMessage, at time.Time) {
	if id == "" || !safeToolName(name) || at.IsZero() {
		d.reset()
		return
	}
	if len(d.pending) >= 128 {
		d.reset()
	}
	c := loopCall{id: id, tool: name, input: loopHash(input), at: at, polling: pollingTool(name, input)}
	if d.candidate != nil {
		expected := d.history[len(d.history)-d.period+len(d.pending)%d.period]
		last := d.history[len(d.history)-1]
		if c.polling || at.Sub(last.at) < 0 || at.Sub(last.at) >= 30*time.Second || c.tool != expected.tool || c.input != expected.input {
			d.candidate = nil
			d.period = 0
		}
	}
	d.pending = append(d.pending, c)
}

func (d *LoopDetector) result(id string, output json.RawMessage, at time.Time) {
	if at.IsZero() {
		// Without a result time there is no way to exclude slow polling.
		d.reset()
		return
	}
	for i := range d.pending {
		if d.pending[i].id == id {
			d.pending[i].result = loopHash(output)
			d.pending[i].resultAt = at
			d.pending[i].ready = true
			break
		}
	}
	for len(d.pending) > 0 && d.pending[0].ready {
		c := d.pending[0]
		d.pending = d.pending[1:]
		d.add(c)
	}
}

func sameLoopCall(a, b loopCall) bool {
	return a.tool == b.tool && a.input == b.input && a.result == b.result
}

func (d *LoopDetector) add(c loopCall) {
	if c.polling {
		d.history = nil
		d.candidate = nil
		d.period = 0
		return
	}
	if len(d.history) > 0 {
		last := d.history[len(d.history)-1]
		resultGap := c.resultAt.Sub(last.resultAt)
		// Concurrent calls may complete out of order; the absolute gap handles either order.
		if delta := c.at.Sub(last.at); delta < 0 || delta >= 30*time.Second || resultGap >= 30*time.Second || resultGap <= -30*time.Second {
			d.history = nil
			d.candidate = nil
			d.period = 0
		}
	}
	d.history = append(d.history, c)
	if len(d.history) > 90 {
		copy(d.history, d.history[len(d.history)-90:])
		d.history = d.history[:90]
	}
	for period := 1; period <= 3; period++ {
		minCount := period * 3
		if period == 1 {
			minCount = 4
		}
		count := 0
		for i := len(d.history) - 1; i >= 0; i-- {
			if !sameLoopCall(d.history[i], d.history[len(d.history)-1-(len(d.history)-1-i)%period]) {
				break
			}
			count++
		}
		if count < minCount {
			continue
		}
		first := d.history[len(d.history)-count]
		start, tool := first.at, first.tool
		if d.candidate != nil && d.period == period && d.candidate.Since.Before(start) {
			start, tool = d.candidate.Since, d.candidate.Tool
			count = d.candidate.Count + 1
		}
		d.candidate = &proto.ToolLoop{Tool: tool, Count: count, Since: start}
		d.period = period
		return
	}
	d.candidate = nil
	d.period = 0
}
