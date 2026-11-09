package screen

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/nwverse/persona/server/internal/lazyre"
)

// Codex's TUI (codex-cli 0.153 / 0.163, a compatible CLI alike) read off its screen: which view has the keyboard, what its input
// box holds, and the two dialogs Persona answers from the phone — an approval ("Would you like to run the following
// command?") and request_user_input's question overlay ("Question 1/2"). Layouts are the ones recorded in
// testdata/codex-*; docs/integrations/codex.md "Questions and approvals".

// Views of a Codex screen.
const (
	CodexComposer   = "composer"       // the input box has the keyboard
	CodexApproval   = "approval"       // an approval with numbered options
	CodexQuestion   = "question"       // request_user_input's overlay (Plan mode)
	CodexAsyncPane  = "async_question" // 0.163's async questions, opened over the input box (⇧← / ⇧→)
	CodexTranscript = "transcript"     // the Ctrl+T pager (0.153): a pending dialog stays hidden under it
	CodexDialog     = "dialog"         // any other choice (trust, a picker): its keys are not ours to guess
	CodexUnknown    = ""
)

// CodexScreen is what a Codex screen shows at the bottom.
type CodexScreen struct {
	View string
	// Draft is what the input box holds (View composer): typed at the computer, a `/command` or an `@file` being picked.
	// "" when the box shows its placeholder.
	Draft    string
	Approval *CodexApprovalDialog
	Question *CodexQuestionDialog
	// Queued are the messages waiting to go in once the running step ends ("↳ …" under the input preview).
	Queued []string
}

// CodexApprovalDialog is "Would you like to …?" with the command, edits or permissions it is about and its options.
type CodexApprovalDialog struct {
	Title   string // "Would you like to run the following command?"
	Reason  string // "Reason: …" without the label
	Command string // the "$ …" lines, joined back where the pane wrapped them
	Detail  string // other lines between the title and the options (edits, permissions, environment)
	Options []CodexOption
	// Rev hashes what it asks: the node checks it again right before pressing a key.
	Rev string
}

// CodexOption is a numbered option of a Codex dialog.
type CodexOption struct {
	Key         string // its number, which picks it
	Label       string // without the "(y)" shortcut hint
	Description string // request_user_input's second column
	Selected    bool   // the › row
}

// Approval option roles (CodexApprovalDialog.Role).
const (
	RoleAllow   = "allow"   // this once
	RoleSession = "session" // and don't ask again in this session (a command, these files, a host)
	RoleAlways  = "always"  // and don't ask again for commands starting with … / this host in the future
	RoleDecline = "decline" // no, and continue without it
	RoleCancel  = "cancel"  // no, and tell Codex what to do differently: the turn stops
)

// Role says what an approval option does, from Codex's wording (tui/src/bottom_pane/approval_overlay.rs, the same in
// 0.153 and 0.163); "" for one Persona does not offer (block a host, strict auto review).
func (o CodexOption) Role() string {
	l := strings.ToLower(o.Label)
	switch {
	case strings.HasPrefix(l, "yes, proceed"), strings.HasPrefix(l, "yes, just this once"),
		strings.HasPrefix(l, "yes, grant these permissions for this turn"), strings.HasPrefix(l, "yes, provide the requested info"):
		return RoleAllow
	case strings.HasPrefix(l, "yes, and don't ask again for commands that start with"),
		strings.HasPrefix(l, "yes, and allow this host in the future"):
		return RoleAlways
	case strings.HasPrefix(l, "yes, and"), strings.HasPrefix(l, "yes, grant these permissions for this session"):
		if strings.Contains(l, "session") || strings.Contains(l, "conversation") || strings.Contains(l, "these files") {
			return RoleSession
		}
	case strings.HasPrefix(l, "no, and tell codex what to do differently"), strings.HasPrefix(l, "cancel this request"):
		return RoleCancel
	case strings.HasPrefix(l, "no, continue without"), strings.HasPrefix(l, "no, but continue without"):
		return RoleDecline
	}
	return ""
}

// Role finds the dialog's option with role, or nil.
func (d *CodexApprovalDialog) Role(role string) *CodexOption {
	for i := range d.Options {
		if d.Options[i].Role() == role {
			return &d.Options[i]
		}
	}
	return nil
}

// CodexQuestionDialog is request_user_input's overlay on its current question.
type CodexQuestionDialog struct {
	Index, Total int // "Question 1/2"
	Question     string
	Options      []CodexOption
	// Notes: the notes field under the options is open (tab, or None of the above picked), maybe with text in it.
	Notes     bool
	NotesText string
}

// Other is the overlay's free-text option ("None of the above"), or nil.
func (q *CodexQuestionDialog) Other() *CodexOption {
	for i := range q.Options {
		if strings.HasPrefix(strings.ToLower(q.Options[i].Label), "none of the above") {
			return &q.Options[i]
		}
	}
	return nil
}

// Selected is the option the cursor is on, or nil.
func (q *CodexQuestionDialog) Selected() *CodexOption {
	for i := range q.Options {
		if q.Options[i].Selected {
			return &q.Options[i]
		}
	}
	return nil
}

// CodexPlaceholder is the empty input box's text: on a screen read without colors it is not a draft.
const CodexPlaceholder = "Ask Codex to do anything"

var (
	codexOptionRE   = lazyre.New(`^(\s*)(›\s+)?(\d+)\.\s+(\S.*)$`)
	codexShortcutRE = lazyre.New(`\s+\((?:[a-z]|esc)\)$`)
	codexQuestionRE = lazyre.New(`^\s*Question (\d+)/(\d+)\b`)
	codexColumnsRE  = lazyre.New(`^(.*?\S)\s{2,}(\S.*)$`)
	codexNumberedRE = lazyre.New(`^›\s*\d+\.\s`)
	codexStatusRE   = lazyre.New(`^\s{2,}\S.*\s·\s(?:~[/\\]|/|[A-Za-z]:[/\\])`)
)

// ReadCodex reads a Codex screen, best read with its colors: without them a draft the user typed cannot be told from
// the box's dim placeholder beyond CodexPlaceholder.
func ReadCodex(screen string) CodexScreen {
	raw := strings.Split(strings.ReplaceAll(screen, "\r", ""), "\n")
	plain := make([]string, len(raw))
	for i, l := range raw {
		plain[i] = strings.TrimRight(strings.ReplaceAll(Plain(l), " ", " "), " ")
	}
	for len(plain) > 0 && strings.TrimSpace(plain[len(plain)-1]) == "" {
		plain, raw = plain[:len(plain)-1], raw[:len(raw)-1]
	}
	if len(plain) == 0 {
		return CodexScreen{}
	}
	last := strings.ToLower(strings.Join(strings.Fields(lastText(plain, 3)), " ")) // a footer the pane wrapped reads whole
	switch {
	case strings.Contains(strings.Join(plain[:min(3, len(plain))], "\n"), "T R A N S C R I P T") && strings.Contains(last, "q to quit"):
		return CodexScreen{View: CodexTranscript}
	case strings.Contains(last, "esc to cancel") && strings.Contains(last, "to confirm"):
		if a := codexApproval(plain); a != nil {
			return CodexScreen{View: CodexApproval, Approval: a}
		}
		return CodexScreen{View: CodexDialog}
	case strings.Contains(last, "enter to submit") || strings.Contains(last, "clear notes"):
		if q := codexQuestion(plain); q != nil {
			return CodexScreen{View: CodexQuestion, Question: q}
		}
	case strings.Contains(last, "main prompt"):
		return CodexScreen{View: CodexAsyncPane}
	}
	if i := codexBox(plain); i >= 0 {
		s := CodexScreen{View: CodexComposer, Draft: codexDraft(raw, plain, i, colorless(screen))}
		for _, l := range plain[max(0, i-30):i] {
			if t := strings.TrimSpace(l); strings.HasPrefix(t, "↳ ") {
				s.Queued = append(s.Queued, strings.TrimPrefix(t, "↳ "))
			}
		}
		return s
	}
	if Choosing(strings.Join(plain, "\n")) {
		return CodexScreen{View: CodexDialog}
	}
	return CodexScreen{}
}

// lastText is the last n non-blank lines, joined in order.
func lastText(lines []string, n int) string {
	var out []string
	for i := len(lines) - 1; i >= 0 && len(out) < n; i-- {
		if strings.TrimSpace(lines[i]) != "" {
			out = append([]string{lines[i]}, out...)
		}
	}
	return strings.Join(out, "\n")
}

// codexBox finds the input box: the last row starting with "› " under which every row is indented (footer, popup,
// the draft's own rows) and none is a picker's selected option. History rows start at the edge ("› " a sent message,
// "• " the agent), so an older one does not pass for the box.
func codexBox(plain []string) int {
	for i := len(plain) - 1; i >= 0; i-- {
		l := plain[i]
		if (l != "" && l[0] != ' ' && !strings.HasPrefix(l, "›")) || selectedRE.MatchString(l) {
			return -1 // history or a picker under any › row: no box on screen
		}
		if strings.HasPrefix(l, "› ") || l == "›" {
			if codexNumberedRE.MatchString(l) {
				return -1
			}
			return i
		}
	}
	return -1
}

// codexDraft is the box's text that is not its dim placeholder: the › row and the rows right under it (a draft of
// several lines), up to the first blank row or the TUI's bottom status. Compact layouts omit the blank separator.
func codexDraft(raw, plain []string, i int, noColor bool) string {
	var rows []string
	end := len(plain)
	if end > i+2 {
		last := strings.TrimSpace(plain[end-1])
		if (strings.HasPrefix(last, "? for shortcuts") || strings.HasPrefix(last, "← for agents · ? for shortcuts")) &&
			codexStatusRE.MatchString(plain[end-2]) {
			// Match the status/path and shortcut footer together at the bottom, not model names or hint text
			// inside a multiline draft. Real text above this pair remains protected from remote input.
			end -= 2
		}
	}
	for k := i; k < end; k++ {
		if k > i && (strings.TrimSpace(plain[k]) == "" || !strings.HasPrefix(plain[k], "  ")) {
			break
		}
		text := plain[k]
		if k == i {
			text = strings.TrimPrefix(text, "›")
		}
		if !noColor {
			text = undimmed(raw[k])
			if k == i {
				text = strings.TrimPrefix(strings.TrimLeft(text, " "), "›")
			}
		}
		if t := strings.TrimSpace(text); t != "" {
			rows = append(rows, t)
		}
	}
	draft := strings.Join(rows, "\n")
	if noColor && draft == CodexPlaceholder {
		return ""
	}
	return draft
}

// undimmed is a colored row's text without its dim runs.
func undimmed(line string) string {
	var b strings.Builder
	for _, r := range sgrRuns(line) {
		if !r.dim {
			b.WriteString(r.text)
		}
	}
	return strings.ReplaceAll(Plain(b.String()), " ", " ")
}

// codexOptions reads numbered rows from start on: label (and description in a second column), the shortcut hint
// dropped, rows the pane wrapped joined back. It stops at the first row that is neither an option nor its wrap.
func codexOptions(plain []string, start int, columns bool) (opts []CodexOption, end int) {
	end = start
	indent := -1
	for k := start; k < len(plain); k++ {
		l := plain[k]
		if m := codexOptionRE.FindStringSubmatch(l); m != nil {
			o := CodexOption{Key: m[3], Label: m[4], Selected: m[2] != ""}
			if columns {
				if c := codexColumnsRE.FindStringSubmatch(o.Label); c != nil {
					o.Label, o.Description = c[1], c[2]
				}
			}
			opts = append(opts, o)
			indent = len([]rune(l)) - len([]rune(strings.TrimLeft(l, " ")))
			end = k + 1
			continue
		}
		t := strings.TrimSpace(l)
		lead := len([]rune(l)) - len([]rune(strings.TrimLeft(l, " ")))
		if len(opts) > 0 && t != "" && lead > indent+2 {
			o := &opts[len(opts)-1]
			if o.Description != "" {
				o.Description = joinWrapped([]string{o.Description, t})
			} else {
				o.Label = joinWrapped([]string{o.Label, t})
			}
			end = k + 1
			continue
		}
		break
	}
	for i := range opts {
		opts[i].Label = codexShortcutRE.ReplaceAllString(opts[i].Label, "")
	}
	return opts, end
}

// codexApproval reads the approval above the footer: its title, then reason / command / other lines, then the options.
func codexApproval(plain []string) *CodexApprovalDialog {
	title := -1
	for i := len(plain) - 1; i >= 0 && i >= len(plain)-80; i-- {
		if strings.HasPrefix(strings.TrimSpace(plain[i]), "Would you like to") {
			title = i
			break
		}
	}
	if title < 0 {
		return nil
	}
	d := &CodexApprovalDialog{}
	k := title
	var titleRows []string
	for ; k < len(plain); k++ {
		titleRows = append(titleRows, strings.TrimSpace(plain[k]))
		if strings.HasSuffix(plain[k], "?") {
			k++
			break
		}
	}
	d.Title = joinWrapped(titleRows)
	first := -1
	for i := k; i < len(plain); i++ {
		if codexOptionRE.MatchString(plain[i]) {
			first = i
			break
		}
	}
	if first < 0 {
		return nil
	}
	var reason, command, detail []string
	field := ""
	for _, l := range plain[k:first] {
		t := strings.TrimSpace(l)
		switch {
		case t == "":
			field = ""
		case strings.HasPrefix(t, "Reason: "):
			field, reason = "reason", append(reason, strings.TrimPrefix(t, "Reason: "))
		case strings.HasPrefix(t, "$ ") && field != "command":
			field, command = "command", append(command, strings.TrimPrefix(t, "$ "))
		case field == "reason":
			reason = append(reason, t)
		case field == "command":
			command = append(command, t)
		default:
			detail = append(detail, strings.TrimRight(strings.TrimPrefix(l, "  "), " "))
		}
	}
	d.Reason, d.Command = joinWrapped(reason), strings.Join(command, "")
	d.Detail = strings.Join(detail, "\n")
	d.Options, _ = codexOptions(plain, first, false)
	if len(d.Options) < 2 {
		return nil
	}
	h := sha256.New()
	h.Write([]byte(d.Title + "\x00" + d.Reason + "\x00" + d.Command + "\x00" + d.Detail))
	for _, o := range d.Options {
		h.Write([]byte("\x00" + o.Key + "\x00" + o.Label))
	}
	d.Rev = hex.EncodeToString(h.Sum(nil))[:12]
	return d
}

// codexQuestion reads request_user_input's overlay: "Question i/n", the question, its options in two columns, and the
// notes field when open.
func codexQuestion(plain []string) *CodexQuestionDialog {
	top := -1
	var m []string
	for i := len(plain) - 1; i >= 0 && i >= len(plain)-60; i-- {
		if m = codexQuestionRE.FindStringSubmatch(plain[i]); m != nil {
			top = i
			break
		}
	}
	if top < 0 {
		return nil
	}
	q := &CodexQuestionDialog{}
	q.Index, _ = strconv.Atoi(m[1])
	q.Total, _ = strconv.Atoi(m[2])
	k := top + 1
	var rows []string
	for ; k < len(plain) && !codexOptionRE.MatchString(plain[k]); k++ {
		if t := strings.TrimSpace(plain[k]); t != "" {
			rows = append(rows, t)
		}
	}
	q.Question = joinWrapped(rows)
	var end int
	q.Options, end = codexOptions(plain, k, true)
	for _, l := range plain[end:] {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "›") {
			q.Notes = true
			if s := strings.TrimSpace(strings.TrimPrefix(t, "›")); s != "Add notes" && s != "Type your answer (optional)" {
				q.NotesText = s
			}
		}
	}
	if strings.Contains(lastText(plain, 1), "clear notes") {
		q.Notes = true
	}
	if len(q.Options) == 0 || q.Total == 0 {
		return nil
	}
	return q
}
