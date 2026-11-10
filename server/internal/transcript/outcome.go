package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"math"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Tool outcomes (proto.ToolOutcome, docs/protocol/protocol.md "Tool outcomes"): what a call came to, on its
// tool_result item. Claude Code records it next to the result (toolUseResult, the line's toolDenialKind) and in the
// fixed words a result opens with; a Codex rollout on the item itself (codexTool, codexStatus). Everything is read from
// the line alone, so a window read from a file's end gives the same outcome (window.go).

// Bounds keep an outcome small next to the page's 4 KB of output (pageOutputLen).
const (
	outcomeTailLen   = 2 * 1024 // the end of an output the page cut: at most this many bytes…
	outcomeTailLines = 30       // …and lines
	outcomePatchLen  = 8 * 1024 // an edit's hunks, whole lines (PatchCut past it)
	outcomeAnswerLen = 1024     // each question and answer of AskUserQuestion
	outcomeAnswers   = 16       // answers kept (AskUserQuestion asks up to 4 questions)
)

// claudeToolResult is the part of a tool_result line's toolUseResult (Claude Code's own record of the call) outcomes
// read. The record also holds the call's whole output (stdout, a Read's content) and an edited file's whole text
// (originalFile): those are skipped, never copied. Its shape varies by tool and version (a failed call records a
// string, an MCP call its content blocks), so UnmarshalJSON takes any value: what is not an object leaves the zero
// value, a field of an unexpected type only that field, and the line is parsed all the same.
type claudeToolResult struct {
	Interrupted              bool     `json:"interrupted"`
	TimedOutAfterMs          *float64 `json:"timedOutAfterMs"`
	BackgroundTaskID         string   `json:"backgroundTaskId"`
	ReturnCodeInterpretation string   `json:"returnCodeInterpretation"` // "No matches found", "Files differ"…
	PersistedOutputPath      string   `json:"persistedOutputPath"`
	PersistedOutputSize      int64    `json:"persistedOutputSize"`
	DurationMs               *float64 `json:"durationMs"` // WebFetch
	GitOperation             *struct {
		Commit *struct {
			SHA    string `json:"sha"`
			Branch string `json:"branch"`
			Kind   string `json:"kind"`
		} `json:"commit"`
		Push *struct {
			Branch string `json:"branch"`
		} `json:"push"`
		PR *struct {
			Number int    `json:"number"`
			URL    string `json:"url"`
			Action string `json:"action"`
		} `json:"pr"`
		Branch *struct {
			Ref    string `json:"ref"`
			Action string `json:"action"`
		} `json:"branch"`
	} `json:"gitOperation"`
	// Edit and Write: the file and Claude Code's diff of the change. Type is Write's create | update (Read's text |
	// file_unchanged | image); Content a new file's text, of which only the lines are counted.
	FilePath        string       `json:"filePath"`
	Type            string       `json:"type"`
	StructuredPatch []claudeHunk `json:"structuredPatch"`
	Content         lineCount    `json:"content"`
	File            *struct {
		StartLine  int `json:"startLine"`
		NumLines   int `json:"numLines"`
		TotalLines int `json:"totalLines"`
	} `json:"file"` // Read
	Answers map[string]string `json:"answers"` // AskUserQuestion
}

type claudeHunk struct {
	OldStart int      `json:"oldStart"`
	OldLines int      `json:"oldLines"`
	NewStart int      `json:"newStart"`
	NewLines int      `json:"newLines"`
	Lines    []string `json:"lines"` // each opens with ' ', '-', '+' or '\' (no newline at end of file)
}

func (r *claudeToolResult) UnmarshalJSON(b []byte) error {
	*r = claudeToolResult{}
	if len(b) == 0 || b[0] != '{' {
		return nil
	}
	type plain claudeToolResult
	if unmarshalLine(b, (*plain)(r)) != nil {
		// unmarshalLine stops at a value of the wrong type; encoding/json goes on past it, keeping the rest.
		*r = claudeToolResult{}
		_ = json.Unmarshal(b, (*plain)(r))
	}
	return nil
}

// denialKind is a line's toolDenialKind (user-rejected, permission-rule…). Anything but a string is read as no kind,
// so a shape Claude Code may write later (an object with a reason) costs the status, not the whole line.
type denialKind string

func (k *denialKind) UnmarshalJSON(b []byte) error {
	var s string
	if unmarshalLine(b, &s) != nil {
		s = ""
	}
	*k = denialKind(s)
	return nil
}

// lineCount is the number of lines of a JSON string, counted in its encoded form (a newline is written \n there),
// so the text itself is never decoded; anything but a string counts 0. A last line without a newline counts.
type lineCount int

func (n *lineCount) UnmarshalJSON(b []byte) error {
	*n = 0
	if len(b) < 3 || b[0] != '"' {
		return nil
	}
	s := b[1 : len(b)-1]
	lines, lineEnd := 1, -1 // lineEnd: where the last newline's escape ends
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' || i+1 == len(s) {
			continue
		}
		i++
		switch {
		case s[i] == 'n':
			lines, lineEnd = lines+1, i
		case s[i] == 'u' && i+4 < len(s) && bytes.EqualFold(s[i+1:i+5], []byte("000a")):
			i += 4
			lines, lineEnd = lines+1, i
		}
	}
	if lineEnd == len(s)-1 { // the text ends in a newline: no line after it
		lines--
	}
	*n = lineCount(lines)
	return nil
}

// Claude Code's own words a tool_result opens with when the call did not run or did not end well (seen in its
// transcripts and its 2.1.3xx binary). toolDenialKind on the line is the reliable sign of a denial; these words are
// the fallback older versions need. A tool writes its own result, so an MCP tool could open with them too: the
// status is only what the apps draw, nothing acts on it.
var (
	claudeExitRe = lazyre.New(`^Exit code (-?\d+)`)
	claudeDenied = []string{
		"The user doesn't want to proceed with this tool use", // declined at the permission prompt
		"The user doesn't want to take this action right now",
		"Permission to use ",               // a deny rule, the permission mode, auto mode
		"Permission for this ",             // Claude Code's own safety check ("Permission for this command was denied…")
		"Claude requested permissions to ", // headless, no grant ("…to write to <path>, but you haven't granted it yet")
	}
	claudeBlocked = []string{
		"<tool_use_error>", // input that failed validation, a guard (the sleep check), a file not read first…
		"This agent is isolated in the worktree ",
	}
	claudeInterrupted   = "[Request interrupted by user for tool use]"
	claudeUnchangedRead = "Wasted call — file unchanged since your last Read"
	claudePersistedRe   = lazyre.New(`\A<persisted-output>\s*\n[^\n]*Full output saved to: ([^\n]+)`)
)

// noToolResult stands for a line with no record of the call, or one for which it cannot tell which result it is.
var noToolResult claudeToolResult

// oneToolResult: blocks hold exactly one tool_result. Claude Code writes each result on a line of its own with its
// record; a line holding several (older versions) has none.
func oneToolResult(blocks []block) bool {
	n := 0
	for _, b := range blocks {
		if b.Type == "tool_result" {
			n++
		}
	}
	return n == 1
}

// claudeOutcome is the outcome of a Claude tool_result: from its text and is_error, and, when the line holds no other
// result, from the line's toolUseResult and toolDenialKind.
func claudeOutcome(text string, isErr bool, r *claudeToolResult, denial string) *proto.ToolOutcome {
	o := &proto.ToolOutcome{Status: proto.OutcomeOK}
	lead := strings.TrimLeft(text, " \t\r\n")
	hasPrefix := func(prefixes []string) bool {
		return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(lead, p) })
	}
	switch {
	case denial == "interrupted" || denial == "cancelled": // Esc mid-call, or the turn aborted
		o.Status = proto.OutcomeInterrupted
	case denial == "user-rejected" || denial == "permission-rule" || strings.HasPrefix(denial, "automode-"):
		o.Status = proto.OutcomeDenied
	case r.Interrupted:
		o.Status = proto.OutcomeInterrupted
	case isErr:
		o.Status = proto.OutcomeFailed
		if m := claudeExitRe.FindStringSubmatch(lead); m != nil {
			if code, err := strconv.Atoi(m[1]); err == nil {
				o.ExitCode = &code
			}
			break
		}
		switch {
		case hasPrefix(claudeBlocked):
			o.Status = proto.OutcomeBlocked
		case hasPrefix(claudeDenied), strings.HasPrefix(lead, "PreToolUse:"): // a hook's "PreToolUse:Bash hook error: …"
			o.Status = proto.OutcomeDenied
		case strings.HasPrefix(lead, claudeInterrupted):
			o.Status = proto.OutcomeInterrupted
		}
	case r.TimedOutAfterMs != nil:
		o.Status = proto.OutcomeTimedOut
	case r.BackgroundTaskID != "":
		o.Status = proto.OutcomeBackground
	case r.ReturnCodeInterpretation == "No matches found":
		o.Status = proto.OutcomeNoMatch
	}
	if ms, ok := milliseconds(r.DurationMs); ok {
		o.DurationMS = &ms
	}
	switch {
	case r.PersistedOutputPath != "":
		o.Persisted = &proto.OutcomePersisted{Path: r.PersistedOutputPath, Size: max(r.PersistedOutputSize, 0)}
	case strings.HasPrefix(lead, "<persisted-output>"):
		if m := claudePersistedRe.FindStringSubmatch(lead); m != nil { // a line with no record says where, not the size
			o.Persisted = &proto.OutcomePersisted{Path: strings.Clone(strings.TrimSpace(m[1]))} // not a slice of the output
		}
	}
	if r.FilePath != "" && (r.StructuredPatch != nil || r.Type == "create") {
		f := proto.OutcomeFile{Path: r.FilePath}
		o.Patch, o.PatchCut, f.Adds, f.Dels = claudePatch(r.StructuredPatch)
		if r.Type == "create" {
			f.Created = true
			if len(r.StructuredPatch) == 0 { // Claude Code records no hunks for a new file: its lines are the adds
				f.Adds = int(r.Content)
			}
		}
		o.Files = []proto.OutcomeFile{f}
	}
	o.Git = claudeGit(r)
	switch {
	case r.Type == "file_unchanged", strings.HasPrefix(lead, claudeUnchangedRead):
		o.Read = &proto.OutcomeRead{Unchanged: true}
	case r.Type == "text" && r.File != nil:
		o.Read = &proto.OutcomeRead{Start: r.File.StartLine, Lines: r.File.NumLines, Total: r.File.TotalLines}
	}
	o.Answers = boundedAnswers(r.Answers)
	if o.Read == nil { // a file's last lines are no conclusion: the file viewer has them
		o.Tail = outputTail(text)
	}
	return o
}

func claudeGit(r *claudeToolResult) *proto.OutcomeGit {
	g := r.GitOperation
	if g == nil {
		return nil
	}
	var out proto.OutcomeGit
	if c := g.Commit; c != nil && c.SHA != "" {
		out.Commit = &proto.OutcomeCommit{SHA: c.SHA, Branch: c.Branch, Kind: c.Kind}
	}
	if p := g.Push; p != nil {
		out.Push = &proto.OutcomePush{Branch: p.Branch}
	}
	if p := g.PR; p != nil && (p.Number > 0 || p.URL != "") {
		out.PR = &proto.OutcomePR{Number: max(p.Number, 0), URL: p.URL, Action: p.Action}
	}
	if b := g.Branch; b != nil && b.Ref != "" {
		out.Branch = &proto.OutcomeBranch{Ref: b.Ref, Action: b.Action}
	}
	if out == (proto.OutcomeGit{}) {
		return nil
	}
	return &out
}

// claudePatch is an edit's hunks as unified diff text with their real line numbers, at most outcomePatchLen bytes of
// whole lines: past that it is cut (cut true) after the last line that fits, and that hunk's header counts only the
// lines kept, so the text stays a valid patch. adds and dels count the whole change.
func claudePatch(hunks []claudeHunk) (patch string, cut bool, adds, dels int) {
	for _, h := range hunks {
		for _, l := range h.Lines {
			switch {
			case strings.HasPrefix(l, "+"):
				adds++
			case strings.HasPrefix(l, "-"):
				dels++
			}
		}
	}
	var b strings.Builder
	for _, h := range hunks {
		header := hunkHeader(h.OldStart, h.OldLines, h.NewStart, h.NewLines)
		keep, size := 0, b.Len()+len(header)
		if b.Len() > 0 {
			size++ // the newline before the header
		}
		for _, l := range h.Lines {
			if size+1+len(l) > outcomePatchLen {
				break
			}
			size += 1 + len(l)
			keep++
		}
		if keep < len(h.Lines) || size > outcomePatchLen {
			cut = true
			if keep == 0 {
				break
			}
			oldN, newN := 0, 0
			for _, l := range h.Lines[:keep] {
				switch {
				case strings.HasPrefix(l, "-"):
					oldN++
				case strings.HasPrefix(l, "+"):
					newN++
				case !strings.HasPrefix(l, `\`):
					oldN, newN = oldN+1, newN+1
				}
			}
			header = hunkHeader(h.OldStart, oldN, h.NewStart, newN)
		}
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.WriteString(header)
		for _, l := range h.Lines[:keep] {
			b.WriteByte('\n')
			b.WriteString(l)
		}
		if cut {
			break
		}
	}
	return b.String(), cut, adds, dels
}

func hunkHeader(oldStart, oldLines, newStart, newLines int) string {
	return fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldLines, newStart, newLines)
}

// boundedAnswers is AskUserQuestion's answers with each question and answer cut to outcomeAnswerLen, at most
// outcomeAnswers of them (in question order, for the same ones on every parse); nil when there are none.
func boundedAnswers(answers map[string]string) map[string]string {
	if len(answers) == 0 {
		return nil
	}
	questions := slices.Sorted(maps.Keys(answers))
	out := make(map[string]string, min(len(answers), outcomeAnswers))
	for _, q := range questions[:min(len(questions), outcomeAnswers)] {
		out[cutUTF8(q, outcomeAnswerLen)] = cutUTF8(answers[q], outcomeAnswerLen)
	}
	return out
}

// cutUTF8 is s cut to at most n bytes on a character boundary, ending in "…" when cut.
func cutUTF8(s string, n int) string {
	if len(s) <= n {
		return s
	}
	end := n - len("…")
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end] + "…"
}

// outputTail is the end of a tool's output that a page cut (light keeps its head, up to pageCut), "" when the page
// carries it whole: its last outcomeTailLines lines, at most outcomeTailLen bytes. It picks up where the page's head
// ends when that is within reach, so a short overflow loses no line; otherwise it starts on a whole line (on a
// character when the end is one long line). Trailing blank lines are left out. It is a copy: a slice would keep the
// whole output alive in every light item a node keeps.
func outputTail(text string) string {
	if len(text) <= pageOutputLen {
		return ""
	}
	head := pageCut(text, pageOutputLen)
	if text[head] == '\n' { // the newline the page's head stops before
		head++
	}
	s := strings.TrimRight(text, " \t\r\n")
	start := max(len(s)-outcomeTailLen, head)
	if start >= len(s) {
		return ""
	}
	if start > head && s[start-1] != '\n' {
		if nl := strings.IndexByte(s[start:], '\n'); nl >= 0 && start+nl+1 < len(s) {
			start += nl + 1
		} else {
			for start < len(s) && !utf8.RuneStart(s[start]) {
				start++
			}
		}
	}
	tail := s[start:]
	for i, n := len(tail)-1, 0; i >= 0; i-- {
		if tail[i] == '\n' {
			if n++; n == outcomeTailLines {
				tail = tail[i+1:]
				break
			}
		}
	}
	return strings.Clone(tail)
}

// milliseconds is a recorded duration in whole milliseconds; ok is false when there is none or it is not a
// non-negative number.
func milliseconds(v *float64) (int64, bool) {
	if v == nil || *v < 0 || math.IsNaN(*v) || *v > math.MaxInt64/2 {
		return 0, false
	}
	return int64(*v), true
}
