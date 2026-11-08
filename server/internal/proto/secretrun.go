package proto

import (
	"fmt"
	"path"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// secret.run: the commands the user approved on the password card, run by the target computer's node with the
// password they typed on its stdin (docs/protocol/protocol.md "hub ↔ node", docs/assistant/assistant.md "Password card").

// MethodSecretRun is the node method.
const MethodSecretRun = "secret.run"

// SecretRunTool is the assistant's tool, and the tool name of its password card: only that tool makes such a card.
const SecretRunTool = "secret_run"

// Limits on a run, the same for the tool that asks it and the node that runs it.
const (
	SecretRunMaxCommands   = 5
	SecretRunMaxCommandLen = 1000 // characters
	SecretRunMaxPassword   = 4096 // bytes
	SecretRunMaxTimeout    = 30 * time.Minute
)

// SecretRunPath is the PATH the commands run with: a sudo (or anything else) earlier in the user's own PATH is not
// what runs; other tools need their absolute paths.
const SecretRunPath = "/usr/bin:/bin:/usr/sbin:/sbin"

// SecretRunParams are secret.run's params. Check alone asks whether the node can run one at all (nothing runs).
type SecretRunParams struct {
	Check    bool     `json:"check,omitempty"`
	Commands []string `json:"commands,omitempty"`
	// Password is base64 in JSON. Whoever builds or decodes it zeroes the slice once the call is over.
	Password  []byte `json:"password,omitempty"`
	TimeoutMS int64  `json:"timeout_ms,omitempty"` // for the whole run
}

// SecretRunAnswer is secret.run's answer to a run: one result per command that ran, in order. The commands after the
// first that fails, times out or cannot start are left out: they did not run.
type SecretRunAnswer struct {
	Results []SecretRunResult `json:"results"`
}

// SecretRunResult is one command's outcome.
type SecretRunResult struct {
	Command  string `json:"command"`
	ExitCode int    `json:"exit_code"` // -1: killed (timed out) or never started
	TimedOut bool   `json:"timed_out"`
	// Output is stdout and stderr together, clipped to its end (Truncated when clipped). When it held the password,
	// none of it comes back (OutputWithheld): where a masked password sits could give a weak one away.
	Output         string `json:"output"`
	Truncated      bool   `json:"truncated"`
	OutputWithheld bool   `json:"output_withheld"`
}

// SecretRunCommandProblem is why command cannot go on a password card, "" when it can. v1 commands are one non-empty
// line of printable ASCII (0x20–0x7E) of at most SecretRunMaxCommandLen characters, so the card shows exactly what
// runs: no control character, no invisible one (a zero-width space in a file name), no look-alike letter or space.
func SecretRunCommandProblem(command string) string {
	if strings.TrimSpace(command) == "" {
		return "it is empty"
	}
	for i, r := range command { // until the first character outside ASCII, i counts characters
		if r < 0x20 || r > 0x7e {
			return fmt.Sprintf("character %d is %U, and only printable ASCII (letters, digits, punctuation, spaces) can go on the card", i+1, r)
		}
	}
	if len(command) > SecretRunMaxCommandLen {
		return fmt.Sprintf("it is longer than %d characters", SecretRunMaxCommandLen)
	}
	return ""
}

// SecretRunPurposeShows: text reads on the card as it is: no control character (Cc), format character (Cf: zero
// width, bidirectional overrides), line or paragraph separator (Zl, Zp), private-use (Co) or unassigned (Cn) code
// point, and valid UTF-8.
func SecretRunPurposeShows(text string) bool {
	for _, r := range text {
		if r == utf8.RuneError || unicode.In(r, unicode.Cc, unicode.Cf, unicode.Zl, unicode.Zp, unicode.Co, unicode.Cn) {
			return false
		}
	}
	return true
}

// ---- sudo -S ----

// SecretRunSudo is how a command runs sudo where a command starts: first, after an operator, and after NAME=value
// assignments or a word in secretRunCommandPrefix. A sudo anywhere else is an argument (which sudo, echo sudo). It is
// a convenience reading, not a parser: a sudo it cannot see (inside sh -c '…', behind an alias) is not counted.
type SecretRunSudo struct {
	Calls        int  // sudo calls
	WithoutStdin bool // one of them has neither -S nor --stdin among its options
	AfterPipe    bool // one of them reads a pipe (… | sudo), not the password
}

// GetsPassword: the command's stdin carries the password: it runs one sudo -S, not after a pipe. Any other command
// gets an empty stdin.
func (s SecretRunSudo) GetsPassword() bool { return s.Calls == 1 && !s.WithoutStdin && !s.AfterPipe }

// SecretRunSudoOf reads how command runs sudo.
func SecretRunSudoOf(command string) SecretRunSudo {
	var s SecretRunSudo
	words := secretRunShellWords(command)
	start, piped := true, false
	for i, w := range words {
		switch {
		case w.op:
			start, piped = true, w.text == "|"
		case !start:
		case !w.quoted && path.Base(w.text) == "sudo":
			s.Calls++
			s.WithoutStdin = s.WithoutStdin || !sudoReadsStdin(words[i+1:])
			s.AfterPipe = s.AfterPipe || piped
			start = false
		case !w.quoted && secretRunCommandPrefix[w.text] || shellAssignment(w.text):
		default:
			start = false
		}
	}
	return s
}

// secretRunCommandPrefix are the words after which a command still starts: reserved words and commands that run the
// next one.
var secretRunCommandPrefix = map[string]bool{"!": true, "{": true, "if": true, "then": true, "else": true, "elif": true,
	"while": true, "until": true, "do": true, "command": true, "env": true, "exec": true, "nohup": true, "time": true}

// shellAssignment: w is NAME=value.
func shellAssignment(w string) bool {
	name, _, ok := strings.Cut(w, "=")
	if !ok || name == "" {
		return false
	}
	for i, c := range name {
		if c != '_' && !('a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || i > 0 && '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// shellWord is a word of a one-line shell command: quotes removed (quoted when any part of it was quoted or escaped),
// or an operator (; & | && || ( ) < > `) that ends a word and may start another command.
type shellWord struct {
	text   string
	quoted bool
	op     bool
}

// secretRunShellWords splits command into words and operators, well enough to find the sudo it calls.
func secretRunShellWords(command string) []shellWord {
	var out []shellWord
	var cur strings.Builder
	inWord, quoted := false, false
	flush := func() {
		if inWord {
			out = append(out, shellWord{text: cur.String(), quoted: quoted})
		}
		cur.Reset()
		inWord, quoted = false, false
	}
	for i := 0; i < len(command); i++ {
		c := command[i]
		switch {
		case c == ' ' || c == '\t':
			flush()
		case (c == '&' || c == '|') && i+1 < len(command) && command[i+1] == c:
			flush()
			out = append(out, shellWord{text: command[i : i+2], op: true})
			i++
		case strings.IndexByte(";&|()<>`", c) >= 0:
			flush()
			out = append(out, shellWord{text: string(c), op: true})
		case c == '\\' && i+1 < len(command):
			i++
			cur.WriteByte(command[i])
			inWord, quoted = true, true
		case c == '\'':
			end := strings.IndexByte(command[i+1:], '\'')
			if end < 0 {
				end = len(command) - i - 1
			}
			cur.WriteString(command[i+1 : i+1+end])
			i += end + 1
			inWord, quoted = true, true
		case c == '"':
			j := i + 1
			for ; j < len(command) && command[j] != '"'; j++ {
				if command[j] == '\\' && j+1 < len(command) {
					j++
				}
			}
			cur.WriteString(command[i+1 : min(j, len(command))])
			i = j
			inWord, quoted = true, true
		default:
			cur.WriteByte(c)
			inWord = true
		}
	}
	flush()
	return out
}

// sudoArgShort / sudoArgLong are sudo's options that take an argument: in a cluster of short options the rest of it
// is the argument, else (and for a long one without =) the next word.
const sudoArgShort = "CDRTUcghprtu"

var sudoArgLong = map[string]bool{"--chdir": true, "--chroot": true, "--close-from": true, "--command-timeout": true,
	"--group": true, "--host": true, "--login-class": true, "--other-user": true, "--prompt": true, "--role": true,
	"--type": true, "--user": true}

// sudoReadsStdin: the options at the start of words (what follows a sudo) include -S, a short cluster with S, or --stdin.
func sudoReadsStdin(words []shellWord) bool {
	for i := 0; i < len(words); i++ {
		w := words[i].text
		if words[i].op || w == "--" || w == "-" || !strings.HasPrefix(w, "-") {
			return false
		}
		if strings.HasPrefix(w, "--") {
			name, _, hasValue := strings.Cut(w, "=")
			if name == "--stdin" {
				return true
			}
			if sudoArgLong[name] && !hasValue {
				i++
			}
			continue
		}
		for j := 1; j < len(w); j++ {
			if w[j] == 'S' {
				return true
			}
			if strings.IndexByte(sudoArgShort, w[j]) >= 0 {
				if j == len(w)-1 {
					i++ // its argument is the next word
				}
				break
			}
		}
	}
	return false
}
