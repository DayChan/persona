package node

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// A start types its command into a new tab whose shell is not the one the launchers were listed in (#1642): herdr's
// panes may run in a container whose ~/.bashrc returns early, another shell than the node's login shell, or a bash
// login shell that reads ~/.bash_profile. That shell answers the command it cannot run with a line of its own within
// a second, and claude never comes; without reading that line the start waited claudeStartWait and blamed a busy
// machine. shellMisses reads those lines, startProbe decides which of them ends the start, and how.

// shellMiss is a shell's own line saying it could not run a command.
type shellMiss struct {
	Word  string // the command it could not run, as the shell names it: a name or a path
	Kind  string // missNotFound, missNoSuchFile, missPermission
	Shell string // the shell the line names (bash, zsh, dash…); "" when it names none (a zsh function, Ubuntu's handler)
	Line  string // the line as printed
}

// shellMiss.Kind values.
const (
	missNotFound   = "not_found"         // no such command
	missNoSuchFile = "no_such_file"      // a path that is not there, or a script whose interpreter is not
	missPermission = "permission_denied" // a path that cannot be run
)

// shellMissLines are the whole lines shells print for a command they cannot run, each with its kind. The first group is
// the line's prefix (a shell, its path, "-bash" for a login shell, a zsh function's name, an rc file), the second the
// command. Shells checked by hand (bash 3.2 and 5, zsh 5.9, dash, ksh93) and by their documentation (fish 2 and 3,
// mksh, busybox, Ubuntu's and Fedora's command-not-found handlers).
var shellMissLines = []struct {
	re   *lazyre.Regexp
	kind string
}{
	// bash (and sh as bash): "bash: x: command not found", "-bash: …", "/bin/bash: …", "bash: line 1: …"; Fedora's
	// handler adds "...".
	{lazyre.New(`^([^\s:]+)(?:: line \d+)?: ([^\s:]+): command not found(?:\.\.\.)?$`), missNotFound},
	// bash's exec builtin: "bash: exec: x: not found".
	{lazyre.New(`^([^\s:]+)(?:: line \d+)?: exec: ([^\s:]+): not found$`), missNotFound},
	// zsh: "zsh: command not found: x", "zsh:1: …", a function's "cc:2: command not found: claude", an rc file's.
	{lazyre.New(`^([^\s:]+)(?::\d+)?: command not found: (\S+)$`), missNotFound},
	// dash and sh as dash: "sh: 1: x: not found", "/bin/dash: 2: /p: not found".
	{lazyre.New(`^([^\s:]+): \d+: ([^\s:]+): not found$`), missNotFound},
	// ksh, mksh, busybox: "ksh: x: not found" (ksh93 may add why).
	{lazyre.New(`^([^\s:]+): ([^\s:]+): not found(?: \[No such file or directory\])?$`), missNotFound},
	// fish 3 ("fish: Unknown command: x") and fish 2 ("fish: Unknown command 'x'").
	{lazyre.New(`^(fish): Unknown command: (\S+)$`), missNotFound},
	{lazyre.New(`^(fish): Unknown command '([^']+)'$`), missNotFound},
	// Ubuntu's handler without a suggestion ("x: command not found") and with one ("Command 'x' not found, did you mean:").
	{lazyre.New(`^()([^\s:]+): command not found$`), missNotFound},
	{lazyre.New(`^()Command '([^']+)' not found\b.*$`), missNotFound},
	// personad launch (a Persona-managed launcher) finding no claude to run. It ends through log.Fatal, whose default
	// flags put the date and time in front: "2026/11/09 02:13:19 personad launch: claude not found in PATH or …".
	{lazyre.New(`^()(?:\d{4}/\d\d/\d\d \d\d:\d\d:\d\d(?:\.\d+)? )?personad launch: (\S+) not found in PATH\b.*$`), missNotFound},
	// A path: bash, sh and dash "…: /p: No such file or directory", zsh "zsh: no such file or directory: /p".
	{lazyre.New(`^([^\s:]+)(?:: line \d+|: \d+)?: ([^\s:]+): No such file or directory$`), missNoSuchFile},
	{lazyre.New(`^([^\s:]+)(?::\d+)?: no such file or directory: (\S+)$`), missNoSuchFile},
	// A script whose interpreter is not there: bash "bash: /p: /i: bad interpreter: …", zsh "zsh: /p: bad interpreter: …".
	{lazyre.New(`^([^\s:]+)(?:: line \d+|:\d+)?: ([^\s:]+): (?:[^\s:]+: )?bad interpreter: .*$`), missNoSuchFile},
	// A path that cannot be run: bash and dash "…: /p: Permission denied", zsh "zsh: permission denied: /p", ksh
	// "ksh: /p: cannot execute [Permission denied]", fish "fish: The file '/p' is not executable by this user".
	{lazyre.New(`^([^\s:]+)(?:: line \d+|: \d+)?: ([^\s:]+): Permission denied$`), missPermission},
	{lazyre.New(`^([^\s:]+)(?::\d+)?: permission denied: (\S+)$`), missPermission},
	{lazyre.New(`^([^\s:]+): ([^\s:]+): cannot execute \[Permission denied\]$`), missPermission},
	{lazyre.New(`^(fish): The file '([^']+)' is not executable by this user$`), missPermission},
}

// shellMissWords are what any of shellMissLines has in it: a screen without one is not read again for them.
var shellMissWords = []string{"not found", "No such file", "no such file", "ermission denied", "Unknown command",
	"bad interpreter", "not executable"}

// mayHaveShellMiss: the screen has a word of a shell's not-found line.
func mayHaveShellMiss(screen string) bool {
	for _, w := range shellMissWords {
		if strings.Contains(screen, w) {
			return true
		}
	}
	return false
}

// parseShellMiss reads line as a shell's not-found line: the whole line must be one (a line of Claude's output that
// quotes one, "⎿ zsh: command not found: x", is not). A path dash, ksh, mksh or busybox says is "not found" is a
// file that is not there, as bash and zsh word it (a managed launcher's personad the terminal cannot see).
func parseShellMiss(line string) (shellMiss, bool) {
	line = strings.TrimSpace(line)
	if line == "" || len(line) > 512 {
		return shellMiss{}, false
	}
	for _, p := range shellMissLines {
		m := p.re.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		shell := strings.TrimSuffix(strings.TrimPrefix(filepath.Base(m[1]), "-"), ".exe")
		if m[1] == "" || !shellProgram(shell) && shell != "mksh" {
			shell = ""
		}
		kind := p.kind
		if kind == missNotFound && strings.Contains(m[2], "/") {
			kind = missNoSuchFile
		}
		return shellMiss{Word: m[2], Kind: kind, Shell: shell, Line: line}, true
	}
	return shellMiss{}, false
}

// shellMissAt is a not-found line on a start's screen, and whether it comes after the last line holding the start's
// anchor: a shell slow to read its rc file has the terminal echo the typed command first, then prints the rc file's
// own lines, then its prompt and the command again; only what comes after that is the command's.
type shellMissAt struct {
	shellMiss
	afterLast bool
}

// fishTrace is a line of fish's backtrace after an error in a function, which repeats the function's arguments (the
// anchor with them): "in function 'cc' with arguments '--session-id …'" (fish 3), "with parameter list '…'" (fish 2).
var fishTrace = lazyre.New(`^\s*(?:in function '[^']*' with arguments '|with parameter list ')`)

// shellMisses are the not-found lines of screen after its first line holding anchor (the session id the start typed):
// what came before it, an adopted pane's older scrollback, is not this start's. fish's backtrace holds the anchor too
// but is no echo of the command: it does not move the last one.
func shellMisses(screen, anchor string) []shellMissAt {
	if anchor == "" {
		return nil
	}
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	first, last := -1, -1
	for i, l := range lines {
		if strings.Contains(l, anchor) {
			if first < 0 {
				first = i
			}
			if !fishTrace.MatchString(l) {
				last = i
			}
		}
	}
	if first < 0 {
		return nil
	}
	var out []shellMissAt
	for i := first + 1; i < len(lines); i++ {
		if strings.Contains(lines[i], anchor) {
			continue
		}
		if m, ok := parseShellMiss(lines[i]); ok {
			out = append(out, shellMissAt{m, i > last})
		}
	}
	return out
}

// startProbe is what waitReady looks for on a start's screen while claude has not come: the tab's shell saying it
// cannot run the start's command (#1642).
type startProbe struct {
	anchor    string // the session id, which the typed command holds
	program   string // the command's first word: the launcher's name, or the personad of a managed one
	launcher  string // the launcher the start runs
	paneShell string // the shell the tab is thought to run (paneShells), when the line names none; "" unknown
	// listed says whether the node lists launcher (nil: it does), asked only when the tab's shell has no such command:
	// one the login shell does not have either (removed from the rc file, a stale name) is no terminal's problem.
	listed func() bool
	// startFile is the start file the typed line sources ("" when the command was typed out), firstWord the typed
	// line's first word: the shell answering for either one did not take the line itself (rejects, #2570).
	startFile, firstWord string
}

// typed has the probe know the line a start typed (typedStart): the start file it sources and its first word.
func (p *startProbe) typed(t *typedStart) {
	if p == nil || t == nil {
		return
	}
	p.startFile = t.path
	if f := strings.Fields(t.line); len(f) > 0 {
		p.firstWord = f[0]
	}
}

// startLineRejected is the error of a start whose typed line the tab's shell did not take: something before the shell's
// line editor (a startup question reading one key) ate its first characters, and the shell answered for the rest
// (#2570). Nothing ran: the start may type the line again (control.go).
type startLineRejected struct{ line string }

func (e *startLineRejected) Error() string {
	return fmt.Sprintf("the new tab's shell did not take the start line: it printed %q", e.line)
}

// rejects: m, a not-found line after the start's typed line, is the shell answering for that line with its head cut
// off: the start file itself (". " eaten, the file is not executable), or a tail of the line's first word ("nset"
// for "unset").
func (p *startProbe) rejects(m shellMissAt) bool {
	if !m.afterLast {
		return false
	}
	if p.startFile != "" && (m.Word == p.startFile || strings.Trim(m.Word, `'"`) == p.startFile) {
		return true
	}
	return p.firstWord != "" && m.Word != "" && len(m.Word) < len(p.firstWord) && strings.HasSuffix(p.firstWord, m.Word)
}

// shellParseErrors are a shell's lines for a typed line it could not parse (zsh, bash, fish), and shellContinuation the
// prompt of one that waits for a quote to close (zsh's quote> / dquote>): what is left of a start line whose head was
// eaten.
var (
	shellParseErrors = []*lazyre.Regexp{
		lazyre.New(`^[^\s:]+(?::\d+)?: parse error near\b`),
		lazyre.New(`^[^\s:]+(?:: line \d+)?: syntax error near unexpected token\b`),
		lazyre.New(`^fish: Unexpected end of string, quotes are not balanced$`),
	}
	shellContinuation = lazyre.New(`^(?:[a-z]+ )*(?:quote|dquote|bquote)>\s*$`)
)

// mayBeRejected: the screen has a word of a shell's parse error or ends on a continuation prompt.
func mayBeRejected(screen string) bool {
	return strings.Contains(screen, "parse error") || strings.Contains(screen, "syntax error") ||
		strings.Contains(screen, "quotes are not balanced") || shellContinuation.MatchString(lastLine(screen))
}

// rejectedLine is the line of screen saying the shell did not take the start's typed line as it was: a parse error
// after the last line holding the anchor, or a continuation prompt at the bottom. "" when there is none.
func (p *startProbe) rejectedLine(screen string) string {
	if p == nil || p.anchor == "" {
		return ""
	}
	lines := strings.Split(strings.ReplaceAll(screen, "\r\n", "\n"), "\n")
	last := -1
	for i, l := range lines {
		if strings.Contains(l, p.anchor) && !fishTrace.MatchString(l) {
			last = i
		}
	}
	if last < 0 {
		return ""
	}
	for _, l := range lines[last+1:] {
		l = strings.TrimSpace(l)
		for _, re := range shellParseErrors {
			if re.MatchString(l) {
				return l
			}
		}
	}
	if l := lastLine(screen); shellContinuation.MatchString(l) {
		return l
	}
	return ""
}

// probeFor is the probe of a start of launcher under session id id, typed into a tab whose shells are shells.
func (n *Node) probeFor(ctx context.Context, id, launcher string, shells []string) *startProbe {
	program := launcher
	if _, ok := n.managedLauncher(launcher); ok {
		program = n.bin
	}
	shell := ""
	for _, sh := range shells {
		if sh != "" { // herdr on Windows names none
			shell = strings.TrimSuffix(strings.TrimPrefix(filepath.Base(sh), "-"), ".exe")
			break
		}
	}
	return &startProbe{anchor: id, program: program, launcher: launcher, paneShell: shell,
		listed: func() bool { return n.launcherListed(ctx, launcher) }}
}

// launcherListed: the node has launcher (plain claude, a managed one, a runtime's, one its login shell lists; the
// list a minute old at most). A list that cannot be read says nothing: taken as listed.
func (n *Node) launcherListed(ctx context.Context, name string) bool {
	if name == "claude" || name == traexAgent {
		return true
	}
	if _, ok := n.managedLauncher(name); ok {
		return true
	}
	if n.runtimes.launcherRuntime(name) != nil {
		return true
	}
	list, err := n.launchers(ctx)
	return err != nil || slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == name })
}

// verdict is what screen says of the start: the refusal it ends with (nil: none yet), and the last other not-found
// line after the command, for the timeout to name. shellBack says the tab's shell is back in front (or that the host
// cannot tell): a missing claude ends the start only then, since a launcher may go on after its first try.
func (p *startProbe) verdict(screen string, shellBack func() bool) (refused error, other string) {
	for _, m := range shellMisses(screen, p.anchor) {
		switch {
		case p.rejects(m):
			return &startLineRejected{line: m.Line}, ""
		case m.Word == p.program:
			// The command itself: only it can print its own name back, wherever the line is (fish reprints the
			// command after its error).
			if p.launcher == "claude" {
				return p.refusal(proto.CodeClaudeMissing, m.shellMiss), ""
			}
			return p.refusal(proto.CodeLauncherMissing, m.shellMiss), ""
		case m.afterLast && filepath.Base(m.Word) == "claude":
			if shellBack() {
				return p.refusal(proto.CodeClaudeMissing, m.shellMiss), ""
			}
		case m.afterLast:
			other = m.Line // an rc file's or PROMPT_COMMAND's noise, or a command the launcher runs first
		}
	}
	return nil, other
}

// startMissWhere is the refusal data's where of a start the tab's shell could not run: the error is the terminal's,
// not the launcher's as the login shell has it (the hub words it so, and launcher health keeps it apart).
const startMissWhere = "terminal"

// refusal is the coded start refusal of m (docs/protocol/protocol.md "Error body"): the hub says it in the user's language
// with the launcher, the shell that could not run it and the shell the launchers are listed in (apierr.explain). A
// launcher the node does not list either is plain launcher_missing: gone from the login shell too, not the terminal's.
func (p *startProbe) refusal(code string, m shellMiss) error {
	pane := m.Shell
	if pane == "" {
		pane = p.paneShell
	}
	tab := "the new tab's shell" // "the new tab's shell (bash)"
	if pane != "" {
		tab += " (" + pane + ")"
	}
	if code == proto.CodeLauncherMissing && m.Kind == missNotFound && p.listed != nil && !p.listed() {
		return refusal(code, fmt.Sprintf("%s has no command %s: it printed %q, and this computer does not list that launcher either "+
			"(removed, or renamed?)", tab, m.Word, m.Line), map[string]any{"launcher": p.launcher, "missing": m.Word, "line": m.Line})
	}
	lshell, rc := launcherShell()
	var msg string
	switch {
	case code == proto.CodeClaudeMissing && p.launcher != "claude":
		msg = fmt.Sprintf("launcher %s ran in the new tab, but %s cannot find %s: it printed %q. "+
			"Install Claude Code where that shell finds it, or put it on that shell's PATH", p.launcher, tab, m.Word, m.Line)
	case code == proto.CodeClaudeMissing:
		msg = fmt.Sprintf("%s cannot find %s: it printed %q. Persona finds it from %s (%s); "+
			"install Claude Code where the terminal's shell finds it, or put it on that shell's PATH", tab, m.Word, m.Line, lshell, rc)
	case m.Kind != missNotFound:
		msg = fmt.Sprintf("%s cannot run %s, which starts launcher %s: it printed %q. "+
			"The terminal runs where that file is missing or cannot be run (a container, another user?)", tab, m.Word, p.launcher, m.Line)
	default:
		msg = fmt.Sprintf("%s has no command %s: it printed %q. Persona lists launchers from %s (%s), "+
			"which has it; the terminal's shell reads other startup files (another shell, a container, or an rc file that returns early). "+
			"Define %s where the terminal's shell reads it, or add it to Persona as a model provider", tab, m.Word, m.Line, lshell, rc, p.launcher)
	}
	data := map[string]any{"launcher": p.launcher, "missing": m.Word, "kind": m.Kind, "line": m.Line,
		"launcher_shell": lshell, "rc_file": rc, "where": startMissWhere}
	if pane != "" { // the hub says "the default shell" without it
		data["pane_shell"] = pane
	}
	return refusal(code, msg, data)
}

// shellBackOrUnknown: the pane's host names only shells in front of it, or cannot say what is in front.
func (n *Node) shellBackOrUnknown(ctx context.Context, pane string) bool {
	h, ref, err := n.paneHost(pane)
	if err != nil {
		return true
	}
	f, ok := h.(host.ForegroundNamer)
	if !ok {
		return true
	}
	names, err := f.PaneForeground(ctx, ref)
	if err != nil || len(names) == 0 {
		return true
	}
	for _, name := range names {
		if !shellProgram(name) {
			return false
		}
	}
	return true
}
