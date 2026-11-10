// Package screen reads Claude Code / Codex terminal screens (herdr pane reads): the input box and the dialogs.
//
// The input-box rules are ported from Ovis (github.com/devswha/ovis, server/claude-input.ts, MIT License,
// Copyright (c) 2026 devswha); see THIRD_PARTY_NOTICES.md.
package screen

import (
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

var (
	ansiRE  = lazyre.New(`\x1b\[[0-?]*[ -/]*[@-~]`)
	oscRE   = lazyre.New(`\x1b\][^\x07\x1b]*(?:\x07|\x1b\\)`)
	solidRE = lazyre.New(`^[─━]{8,}$`)
	// A labeled rule over the input box: a named session (`──── my-session ─`).
	labeledRE = lazyre.New(`^─{2,}\s.*─$`)
	// Claude's IDE pill right after the ❯ (`[⧉ 3 lines selected] `): not input, and clearing keys leave it.
	idePillRE = lazyre.New(`^[ \x{00a0}]?\[⧉ [^\]\n]+\] ?`)
	// A paste or an image Claude folded into a placeholder is content, however it is colored.
	placeholderRE = lazyre.New(`\[(?:Pasted text #\d+|Image #\d+)`)
	// The new-session tip in an empty box, `Try "how does <filepath> work?"`, drawn without color under NO_COLOR.
	tipRowRE = lazyre.New(`^Try "[^"]+"$`)
)

// Plain drops ANSI escapes and carriage returns.
func Plain(s string) string {
	s = oscRE.ReplaceAllString(ansiRE.ReplaceAllString(s, ""), "")
	return strings.ReplaceAll(s, "\r", "")
}

func isRule(line string) bool {
	t := strings.TrimSpace(line)
	return solidRE.MatchString(t) || labeledRE.MatchString(t)
}

// box is Claude Code's input box: the rows between the screen's last two rules.
type box struct {
	plain, raw []string
	clipped    bool // only the bottom rule is on screen (a draft taller than the pane)
}

func inputBox(screen string) *box {
	raw := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	plain := make([]string, len(raw))
	for i, l := range raw {
		plain[i] = strings.TrimRight(Plain(l), " ")
	}
	end := len(plain) - 1
	for end >= 0 && !isRule(plain[end]) {
		end--
	}
	if end < 0 {
		return nil
	}
	start := end - 1
	for start >= 0 && !isRule(plain[start]) {
		start--
	}
	if start < 0 {
		return &box{clipped: true}
	}
	return &box{plain: plain[start+1 : end], raw: raw[start+1 : end]}
}

// AboveInputBox is the screen's rows above Claude Code's input box, as plain text (every row when no box is on
// screen): where its status line is (node/claudestatus.go).
func AboveInputBox(screen string) []string {
	lines := strings.Split(Plain(screen), "\n")
	for i, rules := len(lines)-1, 0; i >= 0; i-- {
		if isRule(lines[i]) {
			if rules++; rules == 2 {
				return lines[:i]
			}
		}
	}
	return lines
}

// run is a stretch of characters drawn with the same dim / inverse state.
type run struct {
	text         string
	dim, inverse bool
}

var escRE = lazyre.New(`\x1b(?:\[([0-?]*)[ -/]*([@-~])|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`)

// sgrRuns splits an ANSI line by dim (SGR 2) and inverse (SGR 7); 38/48/58 colors are skipped whole, so the 2 of
// `38;2;r;g;b` is not dim.
func sgrRuns(line string) []run {
	var out []run
	dim, inverse := false, false
	off := 0
	for _, m := range escRE.FindAllStringSubmatchIndex(line, -1) {
		if m[0] > off {
			out = append(out, run{line[off:m[0]], dim, inverse})
		}
		off = m[1]
		if m[4] < 0 || line[m[4]:m[5]] != "m" {
			continue
		}
		params := "0"
		if m[2] >= 0 && m[3] > m[2] {
			params = line[m[2]:m[3]]
		}
		codes := strings.Split(params, ";")
		for i := 0; i < len(codes); i++ {
			c := atoi(codes[i])
			switch c {
			case 38, 48, 58:
				if i+1 < len(codes) {
					switch atoi(codes[i+1]) {
					case 5:
						i += 2
					case 2:
						i += 4
					}
				}
			case 0:
				dim, inverse = false, false
			case 22:
				dim = false
			case 2:
				dim = true
			case 27:
				inverse = false
			case 7:
				inverse = true
			}
		}
	}
	if off < len(line) {
		out = append(out, run{line[off:], dim, inverse})
	}
	return out
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0
		}
		n = n*10 + int(c-'0')
	}
	return n
}

func blank(r rune) bool { return r == ' ' || r == '\t' || r == ' ' }

// colorless: the read sets no color and no dim anywhere (Claude Code under NO_COLOR).
// It runs on every Codex screen read, so it scans the SGR sequences (ESC [ params m) in place instead of with a regexp.
func colorless(ansi string) bool {
	for i := 0; ; {
		k := strings.Index(ansi[i:], "\x1b[")
		if k < 0 {
			return true
		}
		start := i + k + 2
		end := start
		for end < len(ansi) && (ansi[end] >= '0' && ansi[end] <= '9' || ansi[end] == ';' || ansi[end] == ':') {
			end++
		}
		i = end
		if end == len(ansi) || ansi[end] != 'm' {
			continue // not SGR, or cut before its final byte
		}
		// Each ';' or ':' separated number; an empty one (as in ESC [ ; m) sets nothing.
		for p := start; p < end; p++ {
			c, digits := 0, false
			for ; p < end && ansi[p] != ';' && ansi[p] != ':'; p++ {
				c, digits = c*10+int(ansi[p]-'0'), true
			}
			if digits && colorCode(c) {
				return false
			}
		}
	}
}

// colorCode: the SGR code sets dim, a foreground or a background color.
func colorCode(c int) bool {
	return c == 2 || (c >= 30 && c <= 38) || (c >= 40 && c <= 48) || (c >= 90 && c <= 97) || (c >= 100 && c <= 107)
}

// Input is what Claude Code's input box holds.
type Input struct {
	// Box: the screen has an input box (rules at all). Without one nothing is known about the input.
	Box bool
	// Draft: the box is not known to be empty — typed text, bash mode, a paste placeholder, a box clipped by the pane,
	// or rows that are not a `❯` input. Claude's own grey text (a suggested prompt, its tip) and its cursor are not.
	Draft bool
	// Text is the box's content as shown (plain, rows joined, without the ❯), for telling the user what is there.
	Text string
	// Clearable is the draft a single-row box holds, which ctrl+k ctrl+u empty; "" when it cannot be cleared that way.
	Clearable string
	// Suggestion is the next message Claude proposes after a turn (grey in an otherwise empty box: Tab takes it,
	// Enter sends it); rows it wraps onto are joined with a space. Not the new-session tip (`Try "…"`).
	Suggestion string
}

// ClaudeInput reads the live input box. ansi is the same screen read with colors ("" when it could not be read):
// only colors tell grey suggestions from typed words, so without them any text counts as a draft.
func ClaudeInput(ansi string) Input {
	b := inputBox(ansi)
	if b == nil {
		return Input{}
	}
	in := Input{Box: true}
	if b.clipped {
		in.Draft = true
		return in
	}
	rows := make([]string, len(b.plain))
	for i, r := range b.plain {
		rows[i] = strings.TrimRight(strings.ReplaceAll(r, " ", " "), " ")
	}
	// Without Claude's ❯ (bash mode's ! stays: it says the text would run as a command).
	in.Text = strings.TrimSpace(strings.TrimPrefix(strings.Join(rows, "\n"), "❯"))
	if len(b.plain) == 0 || !strings.HasPrefix(b.plain[0], "❯") {
		in.Draft = true
		return in
	}
	after := strings.TrimPrefix(b.plain[0], "❯")
	afterPill := idePillRE.ReplaceAllString(after, " ")
	rest := strings.Join(append([]string{afterPill}, b.plain[1:]...), "\n")
	if strings.TrimSpace(strings.ReplaceAll(rest, " ", " ")) == "" {
		in.Text = ""
		return in
	}
	if placeholderRE.MatchString(strings.Join(b.plain, " ")) {
		in.Draft = true
		return in
	}
	if len(b.plain) == 1 && tipRowRE.MatchString(strings.TrimSpace(strings.ReplaceAll(afterPill, " ", " "))) && colorless(ansi) {
		return in // the tip under NO_COLOR
	}
	in.Draft = typed(b, after)
	if !in.Draft {
		in.Suggestion = suggestion(b, after)
	}
	if in.Draft && len(b.plain) == 1 {
		row := Plain(b.raw[0])
		a := strings.TrimPrefix(row, "❯")
		if pill := idePillRE.FindString(a); pill != "" {
			a = a[len(pill):]
		} else {
			a = strings.TrimPrefix(strings.TrimPrefix(a, " "), " ")
		}
		if strings.TrimSpace(a) != "" {
			in.Clearable = strings.ReplaceAll(a, " ", " ")
		}
	}
	return in
}

// typed: anything after the ❯ that is not dim, other than Claude's own cursor (inverse, on the first grey character).
func typed(b *box, after string) bool {
	pill := len([]rune(idePillRE.FindString(after)))
	prompt, first, cursor, grey := false, true, false, false
	for _, row := range b.raw {
		for _, r := range sgrRuns(row) {
			for _, c := range r.text {
				if !prompt {
					prompt = c == '❯'
					continue
				}
				if pill > 0 {
					pill--
					continue
				}
				if blank(c) {
					continue
				}
				switch {
				case r.dim:
					grey = true
				case first && r.inverse:
					cursor = true
				default:
					return true
				}
				first = false
			}
		}
	}
	return cursor && !grey // a cursor over a typed character has nothing grey after it
}

// suggestion is the grey text after the ❯ of a box with nothing typed (Claude's cursor may sit inverse on its first
// character), each row trimmed and the rows joined with a space; "" for the new-session tip.
func suggestion(b *box, after string) string {
	pill := len([]rune(idePillRE.FindString(after)))
	prompt, first := false, true
	var rows []string
	for _, row := range b.raw {
		var sb strings.Builder
		for _, r := range sgrRuns(row) {
			for _, c := range r.text {
				if !prompt {
					prompt = c == '❯'
					continue
				}
				if pill > 0 {
					pill--
					continue
				}
				if blank(c) {
					sb.WriteRune(' ')
					continue
				}
				if r.dim || (first && r.inverse) {
					sb.WriteRune(c)
				}
				first = false
			}
		}
		if t := strings.TrimSpace(sb.String()); t != "" {
			rows = append(rows, t)
		}
	}
	s := strings.Join(rows, " ")
	if tipRowRE.MatchString(s) {
		return ""
	}
	return s
}

// BoxStyle is the first row of the input box as its styles alone, for a log line that tells a style the reader does
// not know: each SGR sequence as written (`[2m`), any other escape as `⎋`, and between them how many characters,
// never the characters (a draft is the user's, a link's address may hold a token).
func BoxStyle(ansi string) string {
	b := inputBox(ansi)
	switch {
	case b == nil:
		return "no box"
	case b.clipped:
		return "a box taller than the screen"
	case len(b.raw) == 0:
		return "an empty box"
	}
	row := b.raw[0]
	var parts []string
	off := 0
	count := func(s string) {
		if n := len([]rune(s)); n > 0 {
			parts = append(parts, strconv.Itoa(n))
		}
	}
	for _, m := range escRE.FindAllStringSubmatchIndex(row, -1) {
		count(row[off:m[0]])
		if m[4] >= 0 && row[m[4]:m[5]] == "m" && sgrParams.MatchString(row[m[0]:m[1]]) {
			parts = append(parts, row[m[0]+1:m[1]])
		} else {
			parts = append(parts, "⎋")
		}
		off = m[1]
	}
	count(row[off:])
	return strings.Join(parts, "·")
}

var sgrParams = lazyre.New(`^\x1b\[[0-9;:]*m$`)

// ClearKeys empty a one-row Claude input box: the row after the cursor, then before it (Ovis, measured on 2.1.295).
var ClearKeys = []string{"ctrl+k", "ctrl+u"}

// selectedRE is a cursor on a numbered choice: `❯ 1. Yes` (Claude), `› 1. Allow` (Codex).
var selectedRE = lazyre.New(`^\s*[❯›>]\s*\d+\.\s+\S`)

// footerRE is a dialog's key hint line: `Enter to confirm · Esc to cancel`, `Esc to cancel` under a picker.
var footerRE = lazyre.New(`(?i)\b(?:esc to (?:cancel|go back|close)|enter to (?:confirm|select|set))\b`)

// Choosing: the bottom of the screen is a dialog — a key hint footer, or a numbered choice with a cursor below the
// last rule (above it is history: an old message may read `❯ 1. …`) — where typed text would pick an option or land
// in the dialog's own field.
func Choosing(plain string) bool {
	lines := strings.Split(strings.ReplaceAll(plain, "\r", ""), "\n")
	seen := 0
	for i := len(lines) - 1; i >= 0 && seen < 4; i-- {
		if strings.TrimSpace(lines[i]) == "" {
			continue
		}
		seen++
		if footerRE.MatchString(lines[i]) {
			return true
		}
	}
	for i := len(lines) - 1; i >= 0 && i >= len(lines)-30; i-- {
		if isRule(lines[i]) {
			return false
		}
		if selectedRE.MatchString(lines[i]) {
			return true
		}
	}
	return false
}

// historyRE starts a row of the conversation above a dialog: the user's prompt at the left edge (`❯ fix it`), the
// agent's words (⏺), a tool's output (⎿), a finished turn (✻). A dialog's cursor on a numbered choice can be at the
// left edge too (`❯ 1. Preview`, AskUserQuestion 2.1.316): isHistory leaves it out.
var historyRE = lazyre.New(`^(?:❯ |\s*[⏺⎿✻])`)

// codexHistoryRE is the same on a Codex screen (#3214), which has no rule under its conversation: a row at the left
// edge with the agent's or a status's • (`• Ran …`, `• Working (5s • esc to interrupt)`), the user's › (the input box
// is one too: no dialog is open while it shows), an event's ✔ ✗ ■, or a finished turn's "Worked for 2s • 9:41 PM".
// Codex indents its dialogs under them; the › at the edge on an approval's option is the cursor (selectedRE).
var codexHistoryRE = lazyre.New(`^(?:[•✔✗■] |› )|^\s*Worked for \d`)

// isHistory leaves out the dialog's own cursor rows: a numbered choice (selectedRE) and a multi-select question's
// Submit / Next row, which a ❯ puts at the left edge too (`❯    Submit`).
func isHistory(line string) bool {
	return (historyRE.MatchString(line) || codexHistoryRE.MatchString(line)) &&
		!selectedRE.MatchString(line) && !submitRE.MatchString(strings.TrimSpace(line))
}

// dialogTop is the first row of the dialog at the bottom of the screen, on a plain screen's lines: the first row under
// the highest rule (or a picker's ▔ edge) between the dialog's footer and the conversation above it, else the first row
// after the conversation. Everything above it is the conversation, and every reader of the dialog starts there — an
// agent's own answer is often a numbered list (#2872: its numbers were read as the notice's options, so dismiss_notice
// refused the notice and the phone offered the agent's prose as buttons).
func dialogTop(lines []string) int {
	top := max(0, len(lines)-DialogLines)
	start := top
	for i := len(lines) - 1; i >= top; i-- {
		if isHistory(lines[i]) {
			if start == top {
				start = i + 1
				// A Codex row goes on in the rows right under it, up to a blank line (a prompt wrapped at the pane's
				// width, an answer's list): they are the conversation too (#3214).
				if !historyRE.MatchString(lines[i]) {
					for start < len(lines) && strings.TrimSpace(lines[start]) != "" {
						start++
					}
				}
			}
			break
		}
		if isRule(lines[i]) || pickerEdge(lines[i]) {
			start = i + 1 // the highest so far: keep looking up
		}
	}
	return start
}

// DialogHead is the first line of the dialog at the bottom of a plain screen (Choosing), the first row dialogTop
// gives. An AskUserQuestion dialog gives its question instead. "" when no dialog shows.
func DialogHead(plain string) string {
	if !Choosing(plain) {
		return ""
	}
	// AskUserQuestion: its first row is the header chip or tab bar (`☐ Environment`), and its cursor can be at the left edge.
	if d := ParseDialog(plain); d != nil && d.Question != "" && (len(d.Steps) > 0 || d.Header != "" || d.Multi || d.Review) {
		return d.Question
	}
	lines := strings.Split(strings.ReplaceAll(plain, "\r", ""), "\n")
	for _, l := range lines[dialogTop(lines):] {
		if t := strings.TrimSpace(l); t != "" && !isRule(l) && !pickerEdge(l) {
			return t
		}
	}
	return ""
}

// pickerEdge: the ▔ row over a picker, maybe with a hint in it (`▔▔▔ Ctrl+Y to paste deleted text ▔`).
func pickerEdge(line string) bool { return strings.HasPrefix(strings.TrimSpace(line), "▔▔▔") }

// noticeFooterRE is the key hint of a notice that only asks to be read: `Enter to continue · Esc to cancel`.
var noticeFooterRE = lazyre.New(`(?i)^enter to continue\b(?:\s*·\s*esc to (?:cancel|close|go back))?$`)

// NoticeOnly: the dialog at the bottom of a plain screen is a notice that Enter only closes (Claude Code's auto mode
// billing notice, 2.1.309: "Enter to continue · Esc to cancel") — its footer says nothing else, and it has no
// options, cursor or check boxes. Confirmations, permissions and pickers are never one.
func NoticeOnly(plain string) bool {
	if ParseDialog(plain) != nil { // options it can read: a choice, whatever its footer says
		return false
	}
	lines := strings.Split(strings.ReplaceAll(plain, "\r", ""), "\n")
	foot := -1
	for i := len(lines) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(lines[i]); t != "" {
			if noticeFooterRE.MatchString(t) {
				foot = i
			}
			break
		}
	}
	if foot < 0 {
		return false
	}
	for i := foot - 1; i >= 0 && i >= foot-DialogLines; i-- {
		if isHistory(lines[i]) {
			break
		}
		t := strings.TrimSpace(lines[i])
		if selectedRE.MatchString(lines[i]) || numberedRE.MatchString(lines[i]) || cursorRE.MatchString(lines[i]) ||
			boxRE.MatchString(t) || strings.HasPrefix(t, "❯") || strings.HasPrefix(t, "›") {
			return false
		}
	}
	return true
}

// Refusal codes: why text must not be typed into a pane now.
const (
	DialogOpen = "dialog_open" // a dialog has the keyboard
	InputDraft = "input_draft" // the input box holds something the text would be appended to
	// PromptChanged refuses an answer to a dialog no longer on screen as its card showed it (Dialog.Rev).
	PromptChanged = "prompt_changed"
)

// CheckClaude says whether typing a message into a Claude Code pane is safe: "" when it is, else the refusal code
// and what the input box holds. A screen without an input box or dialog (not drawn yet, not Claude's) is let through.
func CheckClaude(ansi string) (string, Input) {
	if Choosing(Plain(ansi)) {
		return DialogOpen, Input{}
	}
	in := ClaudeInput(ansi)
	if in.Draft {
		return InputDraft, in
	}
	return "", in
}
