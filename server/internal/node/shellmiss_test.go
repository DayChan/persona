package node

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Every shell's own line for a command it cannot run, as the shells print them (bash 3.2 and 5, zsh 5.9, dash and
// ksh93 recorded in a terminal; fish, mksh, busybox and the distributions' handlers from their documentation).
func TestParseShellMiss(t *testing.T) {
	for _, tc := range []struct {
		line              string
		word, kind, shell string
	}{
		// bash, as bash, a login shell, a path, a script, Fedora's handler
		{"bash: cc: command not found", "cc", missNotFound, "bash"},
		{"-bash: cc: command not found", "cc", missNotFound, "bash"},
		{"/bin/bash: cc: command not found", "cc", missNotFound, "bash"},
		{"bash: line 1: cc: command not found", "cc", missNotFound, "bash"},
		{"bash: cc: command not found...", "cc", missNotFound, "bash"},
		{"sh: cc: command not found", "cc", missNotFound, "sh"},
		{"bash: exec: claude: not found", "claude", missNotFound, "bash"},
		{"bash: claude: command not found\r", "claude", missNotFound, "bash"},
		// zsh, at the prompt, with a line number, in a launcher function, in an rc file
		{"zsh: command not found: cc", "cc", missNotFound, "zsh"},
		{"zsh:1: command not found: cc", "cc", missNotFound, "zsh"},
		{"cc:2: command not found: claude", "claude", missNotFound, ""},
		{"cc: command not found: claude", "claude", missNotFound, ""},
		{"/home/u/.zshrc:12: command not found: compdef", "compdef", missNotFound, ""},
		// dash and sh as dash
		{"sh: 1: cc: not found", "cc", missNotFound, "sh"},
		{"/bin/dash: 4: cc: not found", "cc", missNotFound, "dash"},
		// a path dash says is "not found" is a file that is not there (a managed launcher's personad)
		{"dash: 2: /opt/p/personad: not found", "/opt/p/personad", missNoSuchFile, "dash"},
		// ksh, mksh, busybox
		{"ksh: cc: not found", "cc", missNotFound, "ksh"},
		{"/bin/ksh: cc: not found [No such file or directory]", "cc", missNotFound, "ksh"},
		{"mksh: cc: not found", "cc", missNotFound, "mksh"},
		{"ksh: /opt/p/personad: not found", "/opt/p/personad", missNoSuchFile, "ksh"},
		{"sh: /opt/p/personad: not found", "/opt/p/personad", missNoSuchFile, "sh"},
		// fish 3 and fish 2
		{"fish: Unknown command: cc", "cc", missNotFound, "fish"},
		{"fish: Unknown command 'cc'", "cc", missNotFound, "fish"},
		// Ubuntu's handler, without and with a suggestion
		{"cc: command not found", "cc", missNotFound, ""},
		{"Command 'cc' not found, did you mean:", "cc", missNotFound, ""},
		// personad launch of a Persona-managed launcher, as log.Fatal prints it (date and time first) and without them
		{"2026/11/09 02:13:19 personad launch: claude not found in PATH or the usual install locations", "claude", missNotFound, ""},
		{"personad launch: claude not found in PATH or the usual install locations", "claude", missNotFound, ""},
		// a path that is not there
		{"bash: /opt/p/personad: No such file or directory", "/opt/p/personad", missNoSuchFile, "bash"},
		{"sh: 1: /opt/p/personad: No such file or directory", "/opt/p/personad", missNoSuchFile, "sh"},
		{"zsh: no such file or directory: /opt/p/personad", "/opt/p/personad", missNoSuchFile, "zsh"},
		{"zsh:1: no such file or directory: /opt/p/personad", "/opt/p/personad", missNoSuchFile, "zsh"},
		{"bash: /home/u/bin/cc: /usr/bin/python9: bad interpreter: No such file or directory", "/home/u/bin/cc", missNoSuchFile, "bash"},
		{"zsh: /home/u/bin/cc: bad interpreter: /usr/bin/python9: no such file or directory", "/home/u/bin/cc", missNoSuchFile, "zsh"},
		// a path that cannot be run
		{"bash: /opt/p/personad: Permission denied", "/opt/p/personad", missPermission, "bash"},
		{"/bin/dash: 3: /opt/p/personad: Permission denied", "/opt/p/personad", missPermission, "dash"},
		{"zsh: permission denied: /opt/p/personad", "/opt/p/personad", missPermission, "zsh"},
		{"ksh: /opt/p/personad: cannot execute [Permission denied]", "/opt/p/personad", missPermission, "ksh"},
		{"fish: The file '/opt/p/personad' is not executable by this user", "/opt/p/personad", missPermission, "fish"},
	} {
		m, ok := parseShellMiss(tc.line)
		if !ok || m.Word != tc.word || m.Kind != tc.kind || m.Shell != tc.shell {
			t.Errorf("parseShellMiss(%q) = %+v %v, want word %q kind %q shell %q", tc.line, m, ok, tc.word, tc.kind, tc.shell)
		}
	}
	// Not a shell's own line: Claude's output quoting one, prose, a prompt, the command itself.
	for _, line := range []string{
		"",
		"  ⎿  zsh: command not found: cc",
		"● The build failed: bash: cc: command not found",
		"Error: unknown option '--plugin-dir'",
		"~/proj % cc --session-id 0e000000-0000-4000-8000-000000000001",
		"the file was not found",
		"Permission denied (publickey).",
		"Claude Code v2.1.318",
		"2026/11/09 02:13:19 personad launch work: no persona node: dial unix /home/u/.persona/node.sock: connect: no such file",
		"in function 'cc' with arguments '--session-id " + probeID + "'",
	} {
		if m, ok := parseShellMiss(line); ok {
			t.Errorf("parseShellMiss(%q) = %+v, want none", line, m)
		}
	}
}

const probeID = "0e000000-0000-4000-8000-000000000001"

// typed is the command line a start types, as the terminal shows it.
func typed(prompt, launcher string) string {
	return prompt + "unset CLAUDECODE && PERSONA_HOME=/home/u/.persona HERDR_AGENT=claude " + launcher +
		" --session-id " + probeID + " --mcp-config /home/u/.persona/mcp.json"
}

func shellBack() bool { return true }
func shellAway() bool { return false }

// Which lines on a start's screen end it, and as what: the command's own name (anywhere after the command, fish
// reprinting the command after its error), a missing claude once the shell is back after the last echo; never the
// echo itself, older scrollback, rc noise before the command's last echo, or another command's line.
func TestStartProbeVerdict(t *testing.T) {
	p := &startProbe{anchor: probeID, program: "cc", launcher: "cc", paneShell: "bash"}
	for _, tc := range []struct {
		name   string
		screen string
		back   func() bool
		code   string
		word   string
		other  string
	}{
		{"bash", typed("$ ", "cc") + "\nbash: cc: command not found\n$ ", shellBack, proto.CodeLauncherMissing, "cc", ""},
		{"zsh", typed("% ", "cc") + "\nzsh: command not found: cc\n% ", shellBack, proto.CodeLauncherMissing, "cc", ""},
		{"dash", typed("$ ", "cc") + "\nsh: 1: cc: not found\n$ ", shellBack, proto.CodeLauncherMissing, "cc", ""},
		{"fish reprints the command", typed("> ", "cc") + "\nfish: Unknown command: cc\n" + typed("", "cc") + "\n^~^\n> ",
			shellBack, proto.CodeLauncherMissing, "cc", ""},
		{"the command's own line needs no shell in front", typed("$ ", "cc") + "\nbash: cc: command not found\n", shellAway,
			proto.CodeLauncherMissing, "cc", ""},
		{"slow rc: the terminal's echo, rc noise, the prompt and the shell's echo", typed("", "cc") +
			"\n/home/u/.bashrc: line 9: nvm: command not found\n" + typed("$ ", "cc") + "\nbash: cc: command not found\n$ ",
			shellBack, proto.CodeLauncherMissing, "cc", ""},
		{"claude missing in the launcher", typed("% ", "cc") + "\ncc:2: command not found: claude\n% ", shellBack,
			proto.CodeClaudeMissing, "claude", ""},
		{"claude missing while the launcher goes on", typed("% ", "cc") + "\ncc:2: command not found: claude\n", shellAway, "", "", ""},
		{"claude missing in a fish function, its backtrace repeating the arguments", typed("> ", "cc") +
			"\nfish: Unknown command: claude\n/home/u/.config/fish/functions/cc.fish (line 2):\n    claude $argv\n    ^~~~~^\n" +
			"in function 'cc' with arguments '--session-id " + probeID + " --mcp-config /home/u/.persona/mcp.json'\n> ",
			shellBack, proto.CodeClaudeMissing, "claude", ""},
		{"claude missing in rc noise before the shell's echo", typed("", "cc") + "\nbash: claude: command not found\n" +
			typed("$ ", "cc") + "\n", shellBack, "", "", ""},
		{"only the echo", typed("$ ", "cc"), shellBack, "", "", ""},
		{"older scrollback above the command", "$ cc --session-id 0e000000-0000-4000-8000-000000000999\nbash: cc: command not found\n" +
			typed("$ ", "cc") + "\n", shellBack, "", "", ""},
		{"no command on screen", "bash: cc: command not found\n$ ", shellBack, "", "", ""},
		{"a longer name is another command", typed("$ ", "cc") + "\nbash: cc_work: command not found\n$ ", shellBack, "", "",
			"bash: cc_work: command not found"},
		{"PROMPT_COMMAND noise", typed("$ ", "cc") + "\nbash: __git_ps1: command not found\n$ ", shellBack, "", "",
			"bash: __git_ps1: command not found"},
		{"claude's own output quoting a shell", typed("$ ", "cc") + "\n  ⎿  bash: cc: command not found\n", shellBack, "", "", ""},
	} {
		refused, other := p.verdict(tc.screen, tc.back)
		var re *rpc.Error
		if tc.code == "" {
			if refused != nil {
				t.Errorf("%s: refused %v", tc.name, refused)
			}
		} else if !errors.As(refused, &re) || re.Code != tc.code {
			t.Errorf("%s: refused %v, want %s", tc.name, refused, tc.code)
		} else {
			var data map[string]string
			_ = json.Unmarshal(re.Data, &data)
			if data["missing"] != tc.word || data["launcher"] != "cc" || data["where"] != startMissWhere ||
				data["delivered"] != deliveredNone || data["pane_shell"] == "" || data["launcher_shell"] == "" || data["rc_file"] == "" {
				t.Errorf("%s: data %v", tc.name, data)
			}
		}
		if other != tc.other {
			t.Errorf("%s: other %q, want %q", tc.name, other, tc.other)
		}
	}
}

// A managed launcher's command is the node's personad: a path the tab cannot run is the launcher missing there (dash,
// ksh and busybox say "not found" of a path, which is the path wording too); its claude missing is claude missing, as
// personad prints it (log.Fatal: date and time first); the plain claude launcher missing is claude missing.
func TestStartProbeOfManagedAndPlainClaude(t *testing.T) {
	managed := &startProbe{anchor: probeID, program: "/opt/p/personad", launcher: "work", paneShell: "zsh"}
	cmd := "/opt/p/personad launch work"
	for _, line := range []string{"zsh: no such file or directory: /opt/p/personad", "dash: 1: /opt/p/personad: not found",
		"sh: /opt/p/personad: not found"} {
		refused, _ := managed.verdict(typed("% ", cmd)+"\n"+line+"\n% ", shellBack)
		var re *rpc.Error
		if !errors.As(refused, &re) || re.Code != proto.CodeLauncherMissing || !strings.Contains(re.Message, "cannot run /opt/p/personad") ||
			!strings.Contains(string(re.Data), `"kind":"no_such_file"`) {
			t.Errorf("managed, %q: %v %s", line, refused, refusalJSON(refused))
		}
	}
	refused, _ := managed.verdict(typed("% ", cmd)+
		"\n2026/11/09 02:13:19 personad launch: claude not found in PATH or the usual install locations\n% ", shellBack)
	var re *rpc.Error
	if !errors.As(refused, &re) || re.Code != proto.CodeClaudeMissing {
		t.Errorf("managed without claude: %v", refused)
	}
	plain := &startProbe{anchor: probeID, program: "claude", launcher: "claude", paneShell: "bash"}
	refused, _ = plain.verdict(typed("$ ", "claude")+"\nbash: claude: command not found\n$ ", shellAway)
	if !errors.As(refused, &re) || re.Code != proto.CodeClaudeMissing {
		t.Errorf("plain claude: %v", refused)
	}
}

func refusalJSON(err error) string {
	var re *rpc.Error
	if errors.As(err, &re) {
		return string(re.Data)
	}
	return ""
}

// A launcher the node does not list either (gone from the rc file, a stale name) is plain launcher_missing: no
// "where", so it is not worded or kept as the terminal's. Listed or not is asked only of a name the shell lacks.
func TestStartProbeOfUnlistedLauncher(t *testing.T) {
	asked := 0
	p := &startProbe{anchor: probeID, program: "cc", launcher: "cc", paneShell: "bash", listed: func() bool { asked++; return false }}
	refused, _ := p.verdict(typed("$ ", "cc")+"\nbash: cc: command not found\n$ ", shellBack)
	var re *rpc.Error
	if !errors.As(refused, &re) || re.Code != proto.CodeLauncherMissing || strings.Contains(string(re.Data), `"where"`) ||
		!strings.Contains(re.Message, "does not list that launcher") || asked != 1 {
		t.Errorf("unlisted: %v %s (asked %d)", refused, refusalJSON(refused), asked)
	}
	refused, _ = p.verdict(typed("$ ", "cc")+"\ncc:2: command not found: claude\n$ ", shellBack)
	if !errors.As(refused, &re) || re.Code != proto.CodeClaudeMissing || !strings.Contains(string(re.Data), `"where":"terminal"`) || asked != 1 {
		t.Errorf("claude missing asked the list: %v (asked %d)", refused, asked)
	}
	p.listed = func() bool { return true }
	refused, _ = p.verdict(typed("$ ", "cc")+"\nbash: cc: command not found\n$ ", shellBack)
	if !errors.As(refused, &re) || !strings.Contains(string(re.Data), `"where":"terminal"`) {
		t.Errorf("listed: %v", refused)
	}
}

// A tab whose shell is not known (herdr on Windows names none) and a line naming none: no shell is made up; the hub
// says "the default shell" in the user's language (apierr.Sentence).
func TestStartProbeOfUnknownShell(t *testing.T) {
	if p := (&Node{}).probeFor(t.Context(), probeID, "cc", []string{"", "/bin/zsh"}); p.paneShell != "zsh" {
		t.Errorf("paneShell %q", p.paneShell)
	}
	if p := (&Node{}).probeFor(t.Context(), probeID, "cc", []string{""}); p.paneShell != "" {
		t.Errorf("paneShell %q of no shell", p.paneShell)
	}
	p := &startProbe{anchor: probeID, program: "cc", launcher: "cc"}
	refused, _ := p.verdict(typed("$ ", "cc")+"\ncc: command not found\n$ ", shellBack)
	var re *rpc.Error
	if !errors.As(refused, &re) || strings.Contains(string(re.Data), "pane_shell") || strings.Contains(re.Message, "()") ||
		strings.Contains(re.Message, "its shell") || !strings.HasPrefix(re.Message, "the new tab's shell has no command cc") {
		t.Errorf("unknown shell: %v %s", refused, refusalJSON(refused))
	}
}

// The refusal says what the shell printed and where the launchers are listed, and names the tab's shell from the line
// (else the shell the tab is thought to run).
func TestStartProbeRefusalWords(t *testing.T) {
	p := &startProbe{anchor: probeID, program: "cc", launcher: "cc", paneShell: "zsh"}
	refused, _ := p.verdict(typed("$ ", "cc")+"\nbash: cc: command not found\n$ ", shellBack)
	var re *rpc.Error
	if !errors.As(refused, &re) {
		t.Fatalf("refused %v", refused)
	}
	lshell, rc := launcherShell()
	for _, want := range []string{"(bash)", `"bash: cc: command not found"`, lshell + " (" + rc + ")", "Define cc"} {
		if !strings.Contains(re.Message, want) {
			t.Errorf("message %q lacks %q", re.Message, want)
		}
	}
	refused, _ = p.verdict(typed("$ ", "cc")+"\ncc: command not found\n$ ", shellBack)
	if !errors.As(refused, &re) || !strings.Contains(string(re.Data), `"pane_shell":"zsh"`) {
		t.Errorf("a line naming no shell: %v %s", refused, re.Data)
	}
}

// The assistant's headless start reads the same lines: dash, ksh and fish name the missing command too.
func TestMissingFromOtherShells(t *testing.T) {
	for _, tc := range []struct {
		launcher string
		line     string
		want     string
	}{
		{"cc_gone", "sh: 1: cc_gone: not found", proto.CodeLauncherMissing},
		{"cc_gone", "ksh: cc_gone: not found", proto.CodeLauncherMissing},
		{"cc_gone", "fish: Unknown command: cc_gone", proto.CodeLauncherMissing},
		{"cc_work", "fish: Unknown command: claude", proto.CodeClaudeMissing},
		{"cc_work", "bash: /opt/x/claude: No such file or directory", proto.CodeClaudeMissing},
	} {
		if got := missingFrom(tc.launcher, []string{tc.line}, true); got != tc.want {
			t.Errorf("missingFrom(%s, %q) = %q, want %q", tc.launcher, tc.line, got, tc.want)
		}
	}
}

// What the shell says of a start line whose head something ate (#2570): the start file itself refused (". " eaten),
// a tail of the line's first word not found ("nset" for "unset"), a parse error, or a quote left open. An rc file's
// noise after the line is none of them, nor is anything before the line.
func TestStartProbeSeesARejectedLine(t *testing.T) {
	const id, file = "0b7c1e52-3a4d-4f6e-8a9b-1c2d3e4f5a6b", "/home/me/.persona/agent-start-123.sh"
	probe := func(firstWord string) *startProbe {
		return &startProbe{anchor: id, program: "cc", launcher: "cc", startFile: file, firstWord: firstWord}
	}
	back := func() bool { return true }
	for _, tc := range []struct {
		name, firstWord, screen, rejected string
	}{
		{"start file", ".", "% '" + file + "' && true '" + id + "'\nzsh: permission denied: " + file + "\n% ",
			"zsh: permission denied: " + file},
		{"first word's tail", "unset", "% nset PERSONA_ASSISTANT && true '" + id + "'\nzsh: command not found: nset\n% ",
			"zsh: command not found: nset"},
		{"parse error", ".", "% && true '" + id + "'\nzsh: parse error near `&&'\n% ", "zsh: parse error near `&&'"},
		{"open quote", ".", "% ersona/agent-start-123.sh' && true '" + id + "'\nquote> ", "quote>"},
		{"rc noise", ".", "$ . '" + file + "' && true '" + id + "'\nbash: __git_ps1: command not found\n$ ", ""},
		{"before the line", ".", "zsh: command not found: nset\nzsh: parse error near `&&'\n% . '" + file + "' && true '" + id + "'\n", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := probe(tc.firstWord)
			refused, _ := p.verdict(tc.screen, back)
			got := ""
			var r *startLineRejected
			if errors.As(refused, &r) {
				got = r.line
			} else if refused != nil {
				t.Fatalf("refused otherwise: %v", refused)
			} else {
				got = p.rejectedLine(tc.screen)
			}
			if got != tc.rejected {
				t.Errorf("rejected %q, want %q", got, tc.rejected)
			}
		})
	}
}
