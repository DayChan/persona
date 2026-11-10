// Package scriptcli is `personad ask` and `personad notify`: a script's way to the phone, through the node on this
// machine (node/scriptcli.go; the design is docs/integrations/script-cli.md).
//
//	personad ask "question" [--options a,b,c] [--timeout 10m] [--require-remote]
//	    blocks until it is answered on the phone. With options the phone draws a question card and the chosen option is
//	    printed; without, an allow / deny card. Exit codes: 0 allowed / an option chosen, 1 denied or handed back,
//	    2 usage, 3 timeout, 4 the phone cannot be reached, 130 interrupted. On 4 (and on a card handed back to the computer) the question
//	    is asked in the terminal when stdin is one, unless --require-remote.
//	personad notify "title" [body] [--silent] [--error] [--strict]
//	    one notification. Best effort: exit 0 whatever happens, unless --strict (the hub must take it, else exit 1).
//
// Both are attributed to the herdr pane the caller runs in (HERDR_PANE_ID and its siblings, nodeclient.HookPane).
package scriptcli

import (
	"bufio"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"golang.org/x/term"
)

// Exit codes of ask.
const (
	ExitOK          = 0
	ExitDenied      = 1 // denied, or handed back to the computer with nobody to ask there
	ExitUsage       = 2
	ExitTimeout     = 3
	ExitUnreachable = 4   // no node, no hub, or the hub did not take the question
	ExitInterrupted = 130 // Ctrl-C / SIGTERM: the phone's card is withdrawn (no terminal fallback), or the terminal's question ends
)

const askUsage = `usage: personad ask "question" [--options a,b,c] [--timeout 10m] [--require-remote]
  exit: 0 allowed / option chosen (printed), 1 denied, 2 usage, 3 timeout, 4 phone unreachable`

const notifyUsage = `usage: personad notify "title" [body] [--silent] [--error] [--strict]
  exit: 0 always (best effort), with --strict 1 when the hub did not take it, 2 usage`

// Runner is what the commands need from the machine they run on (replaced by tests).
type Runner struct {
	Dial   nodeclient.Dialer
	Getenv func(string) string
	Getwd  func() (string, error)
	// IsTerminal says whether r is a terminal a person types in.
	IsTerminal func(io.Reader) bool
}

// Default is the machine this process runs on.
func Default() Runner {
	return Runner{Dial: nodeclient.DialInterruptible, Getenv: os.Getenv, Getwd: os.Getwd, IsTerminal: isTerminal}
}

// isTerminal: a person types at r (not /dev/null, which is a character device too).
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// split gets the flags out from between the arguments (flag stops at the first argument, and these are written
// `ask "question" --options a,b`); everything after a bare "--" is an argument.
func split(fs *flag.FlagSet, args []string) ([]string, error) {
	var rest []string
	for i, a := range args {
		if a == "--" {
			rest, args = args[i+1:], args[:i]
			break
		}
	}
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			break
		}
		pos, args = append(pos, args[0]), args[1:]
	}
	return append(pos, rest...), nil
}

// parseTimeout reads "90" (seconds) or a duration ("10m").
func parseTimeout(s string) (time.Duration, error) {
	if n, err := strconv.Atoi(s); err == nil {
		return time.Duration(n) * time.Second, nil
	}
	return time.ParseDuration(s)
}

func newFlags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard) // the usage text is ours
	return fs
}

// Ask runs `personad ask` and returns its exit code.
func Ask(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	return Default().Ask(ctx, args, stdin, stdout, stderr)
}

// Notify runs `personad notify` and returns its exit code.
func Notify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	return Default().Notify(ctx, args, stdout, stderr)
}

// Ask runs `personad ask` and returns its exit code.
func (r Runner) Ask(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := newFlags("ask")
	options := fs.String("options", "", "")
	timeout := fs.String("timeout", "", "")
	remote := fs.Bool("require-remote", false, "")
	pos, err := split(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, askUsage)
		return ExitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "personad ask: %v\n%s\n", err, askUsage)
		return ExitUsage
	}
	if len(pos) != 1 {
		fmt.Fprintf(stderr, "personad ask: exactly one question, quoted\n%s\n", askUsage)
		return ExitUsage
	}
	req := nodeclient.AskRequest{Question: pos[0]}
	req.Options = strings.Split(*options, ",") // Normalize drops the empty and repeated ones
	if *timeout != "" {
		d, err := parseTimeout(*timeout)
		if err != nil || d < time.Second {
			fmt.Fprintf(stderr, "personad ask: --timeout is seconds or a duration like 10m, at least 1s\n")
			return ExitUsage
		}
		req.TimeoutSeconds = int(d / time.Second)
	}
	if err := req.Normalize(); err != nil {
		fmt.Fprintf(stderr, "personad ask: %v\n", err)
		return ExitUsage
	}
	req.Cwd, _ = r.Getwd()

	// The node answers by the card's expiry; the margin is the time it may spend reconnecting to a restarted hub.
	parent := ctx
	ctx, cancel := context.WithTimeout(ctx, req.Timeout()+nodeclient.ResumeWait+30*time.Second)
	defer cancel()
	rep, err := nodeclient.AskNode(ctx, r.Dial, req, nodeclient.HookPane(r.Getenv))
	if parent.Err() != nil { // interrupted: hanging up on the node withdrew the card
		return ExitInterrupted
	}
	status := rep.Status
	switch {
	case err != nil:
		status = nodeclient.AskUnavailable
		if errors.Is(err, nodeclient.ErrNoNode) {
			rep.Error = "没有在运行的 Persona node（hub 没连上这台电脑）"
		} else {
			rep.Error = err.Error()
		}
	case status == "": // a node that does not know ask
		status, rep.Error = nodeclient.AskUnavailable, "这台电脑上的 personad 太旧，不认识 ask"
	}
	switch status {
	case nodeclient.AskAnswered:
		if rep.Answer != "" {
			fmt.Fprintln(stdout, rep.Answer)
		}
		return ExitOK
	case nodeclient.AskDenied:
		return ExitDenied
	case nodeclient.AskTimeout:
		fmt.Fprintln(stderr, "personad ask: 超时，手机上没人回答")
		return ExitTimeout
	case nodeclient.AskInvalid:
		fmt.Fprintf(stderr, "personad ask: %s\n", rep.Error)
		return ExitUsage
	case nodeclient.AskDismissed: // handed back to the computer
		if *remote || !r.IsTerminal(stdin) {
			fmt.Fprintln(stderr, "personad ask: 手机上选了「交回电脑」")
			return ExitDenied
		}
		fmt.Fprintln(stderr, "personad ask: 手机上选了「交回电脑」，在终端里问")
	default: // unavailable
		if *remote || !r.IsTerminal(stdin) {
			fmt.Fprintf(stderr, "personad ask: 联系不上手机：%s\n", rep.Error)
			return ExitUnreachable
		}
		fmt.Fprintf(stderr, "personad ask: 联系不上手机（%s），在终端里问\n", rep.Error)
	}
	return askTerminal(parent, req, stdin, stderr, stdout) // parent: the phone's deadline does not limit the person typing
}

// askTerminal asks the question where the person is: a numbered list of the options, or y/N. EOF is a no; ctx ending
// (Ctrl-C / SIGTERM) is ExitInterrupted.
func askTerminal(ctx context.Context, req nodeclient.AskRequest, stdin io.Reader, prompt, stdout io.Writer) int {
	in := bufio.NewReader(stdin)
	fmt.Fprintln(prompt, req.Question)
	for i, o := range req.Options {
		fmt.Fprintf(prompt, "  %d) %s\n", i+1, o)
	}
	for {
		if len(req.Options) == 0 {
			fmt.Fprint(prompt, "[y/N] ")
		} else {
			fmt.Fprint(prompt, "> ")
		}
		line, err := readLine(ctx, in)
		if ctx.Err() != nil { // interrupted, even when a line came in at the same moment
			fmt.Fprintln(prompt) // the shell's prompt starts on a line of its own
			return ExitInterrupted
		}
		line = strings.TrimSpace(line)
		if len(req.Options) == 0 {
			if strings.EqualFold(line, "y") || strings.EqualFold(line, "yes") {
				return ExitOK
			}
			return ExitDenied
		}
		if n, convErr := strconv.Atoi(line); convErr == nil && n >= 1 && n <= len(req.Options) {
			fmt.Fprintln(stdout, req.Options[n-1])
			return ExitOK
		}
		for _, o := range req.Options {
			if strings.EqualFold(line, o) {
				fmt.Fprintln(stdout, o)
				return ExitOK
			}
		}
		if err != nil {
			return ExitDenied
		}
	}
}

// readLine reads one line from in, or gives up when ctx ends: signal.NotifyContext keeps Ctrl-C from killing the
// process, so a read nobody answers would wait forever. The abandoned read stays blocked on stdin; the command exits
// right after, as with the hub's readPeerCode.
func readLine(ctx context.Context, in *bufio.Reader) (string, error) {
	type read struct {
		line string
		err  error
	}
	got := make(chan read, 1)
	go func() {
		line, err := in.ReadString('\n')
		got <- read{line, err}
	}()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case r := <-got:
		return r.line, r.err
	}
}

// Notify runs `personad notify` and returns its exit code.
func (r Runner) Notify(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	fs := newFlags("notify")
	silent := fs.Bool("silent", false, "")
	isErr := fs.Bool("error", false, "")
	strict := fs.Bool("strict", false, "")
	pos, err := split(fs, args)
	if errors.Is(err, flag.ErrHelp) {
		fmt.Fprintln(stdout, notifyUsage)
		return ExitOK
	}
	if err != nil {
		fmt.Fprintf(stderr, "personad notify: %v\n%s\n", err, notifyUsage)
		return ExitUsage
	}
	if len(pos) < 1 || len(pos) > 2 || strings.TrimSpace(pos[0]) == "" {
		fmt.Fprintf(stderr, "personad notify: a title and at most one body, each quoted\n%s\n", notifyUsage)
		return ExitUsage
	}
	req := nodeclient.NotifyRequest{Title: pos[0], Silent: *silent, Error: *isErr}
	if len(pos) == 2 {
		req.Body = pos[1]
	}

	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	rep, err := nodeclient.NotifyNode(ctx, r.Dial, req, nodeclient.HookPane(r.Getenv))
	lang := i18n.From(ctx).Or(i18n.Default)
	why := ""
	switch {
	case errors.Is(err, nodeclient.ErrNoNode):
		why = "没有在运行的 Persona node"
	case err != nil:
		why = err.Error()
	case rep.Error != "":
		why = rep.Error
	case rep.Queued: // kept on this computer until its manager is back: not sent yet
		if !*strict {
			return ExitOK
		}
		why = "manager 不在线，node 先存着，回来后再发"
	case !rep.Sent:
		switch rep.Reason {
		case "no_device":
			why = i18n.T(lang, "scriptcli-notify.no_device")
		case "filtered":
			why = i18n.T(lang, "scriptcli-notify.filtered")
		default:
			why = i18n.T(lang, "scriptcli-notify.hub_not_sent", i18n.Args{"reason": rep.Reason})
		}
	}
	if why == "" {
		return ExitOK
	}
	fmt.Fprintf(stderr, "personad notify: %s\n", i18n.T(lang, "scriptcli-notify.not_sent", i18n.Args{"reason": why}))
	if *strict {
		return ExitDenied
	}
	return ExitOK
}
