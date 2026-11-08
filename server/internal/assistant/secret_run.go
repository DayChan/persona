package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/proto"
)

// secret_run: commands that need a password only the user knows (a computer's sudo password). The card shows the
// computer, the purpose and every command verbatim; the user types the password into it on the iPhone, and the target
// computer's node runs exactly those commands with it on their stdin (proto.MethodSecretRun). The Agent gets exit codes
// and redacted output, never the password (docs/assistant/assistant.md "Password card", #1137).
//
// The password lives only in this call while it waits on the node: it is never logged, put in an error, kept for
// another try (askOnPhone) or written to the history (redacted), and the byte slice handed to the node is zeroed.

// secretRunCardWait is how long the card waits to be filled in (a variable so tests shorten it).
var secretRunCardWait = 10 * time.Minute

const (
	secretRunDefaultTimeout = 300 * time.Second
	secretRunCheckTimeout   = 30 * time.Second
	// secretRunMargin is how much longer than the run's own timeout the node's answer is waited for.
	secretRunMargin = 30 * time.Second
	// secretRunOutputRunes is how much of each command's output (its end) the Agent gets.
	secretRunOutputRunes = 4000
	secretRunMaxPurpose  = 300 // characters
	secretRunField       = "password"
)

// Statuses of a secret_run result.
const (
	secretRan      = "ran"
	secretFailed   = "failed"
	secretDeclined = "declined"
	secretExpired  = "expired"
	secretRefused  = "refused"
)

type secretRunArgs struct {
	Machine        string   `json:"machine"`
	Purpose        string   `json:"purpose"`
	Commands       []string `json:"commands"`
	Delivery       string   `json:"delivery"`
	TimeoutSeconds int      `json:"timeout_seconds"`
}

// secretRunOutcome is the tool's result (JSON text, never an error once the card was shown).
type secretRunOutcome struct {
	Status  string                  `json:"status"`
	Machine string                  `json:"machine"`
	Message string                  `json:"message"`
	Results []proto.SecretRunResult `json:"results,omitempty"`
}

func (s *Service) toolSecretRun(ctx context.Context, machine, self string, args json.RawMessage) (string, error) {
	var a secretRunArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	a.Purpose = strings.TrimSpace(a.Purpose)
	timeout, err := checkSecretRunArgs(a)
	if err != nil {
		return "", err
	}
	m, ok := s.machineByName(ctx, a.Machine)
	if !ok {
		return "", fmt.Errorf("unknown machine %q (machines: %s)", a.Machine, s.machineIDs(ctx))
	}
	// Before the user types anything: an offline computer, a node too old for it or a Windows one refuses here.
	cctx, cancel := context.WithTimeout(ctx, secretRunCheckTimeout)
	var check struct {
		OK bool `json:"ok"`
	}
	err = s.call(cctx, m.ID, proto.MethodSecretRun, proto.SecretRunParams{Check: true}, &check)
	cancel()
	if err == nil && !check.OK {
		err = errors.New("its node did not confirm it can")
	}
	if err != nil {
		return "", fmt.Errorf("%s cannot run commands with a password now: %v. Nothing was asked of the user", m.Name, err)
	}
	l := s.lang(ctx)
	name := secretRunMachineLabel(m, s.devices(ctx))
	input, _ := json.Marshal(map[string]any{"machine": m.ID, "machine_name": name, "purpose": a.Purpose, "commands": a.Commands,
		"delivery": "stdin"})
	now := time.Now().UTC()
	expires := now.Add(secretRunCardWait)
	r, err := s.ask(ctx, proto.Interaction{ID: "ix_" + randomHex(6), MachineID: machine, SessionID: self, SessionTitle: "Persona",
		Kind: proto.IxPermission, ToolName: proto.SecretRunTool, ToolInput: input, Summary: secretRunSummary(l, name, a.Purpose, a.Commands),
		Fields: []proto.Field{{Key: secretRunField, Label: i18n.T(l, "assistant-secret_run.password"),
			Placeholder: i18n.T(l, "assistant-secret_run.password_hint"), Secret: true}},
		CreatedAt: now, ExpiresAt: expires})
	if err != nil {
		return "", err
	}
	out := secretRunOutcome{Machine: name}
	password := r.Values[secretRunField]
	r.Values = nil
	switch {
	case r.AnsweredBy != "" || r.Remembered: // not the user's own answer on the card (interaction_answer, a kept "Always")
		out.Status, out.Message = secretRefused, secretRefusedText
	case r.Allow != nil && !*r.Allow:
		out.Status, out.Message = secretDeclined, "The user declined on the phone: nothing ran."
	case r.Dismiss:
		out.Status, out.Message = secretRunDismissal(time.Now(), expires)
	case r.Allow == nil, strings.TrimSpace(password) == "", password == redactedValue: // a replay only ever has the marker
		out.Status, out.Message = secretRefused, secretRefusedText
	case strings.ContainsFunc(password, unicode.IsControl): // sudo would read up to a line break, the rest left on stdin
		out.Status, out.Message = secretRefused, "The password the user typed has a control character (a line break, a "+
			"tab): sudo would take only part of it, so nothing ran. They can type it again on a new card."
	case len(password) > proto.SecretRunMaxPassword: // the node would refuse the run before it starts
		out.Status, out.Message = secretRefused, fmt.Sprintf("The password the user typed is longer than %d bytes, more "+
			"than a run takes: nothing ran.", proto.SecretRunMaxPassword)
	default:
		ans, err := s.runSecret(ctx, m.ID, a.Commands, password, timeout)
		if err != nil {
			out.Status = secretFailed
			out.Message = fmt.Sprintf("The run on %s failed after the user approved: %v. Some commands may have run; check "+
				"before asking the user again. %s", name, err, secretRunUsed)
		} else {
			out.Results = secretRunResults(a.Commands, ans.Results, password)
			out.Status, out.Message = secretRunVerdict(name, len(a.Commands), out.Results, timeout)
		}
	}
	b, err := managerconf.MarshalNoEscape(out) // commands with && or > read as written
	return string(b), err
}

const (
	secretRefusedText = "The card was answered where the password cannot be typed in (only the iPhone app fills it in), or " +
		"without the password: nothing ran."
	secretRunUsed = "The password was used once and discarded."
)

// secretRunExpirySlack: a dismiss this close to the card's deadline is its expiry. Whatever expires the card (the
// manager's timer, or the hub's queue on its own clock) may say so a moment before the deadline by this clock.
const secretRunExpirySlack = 2 * time.Second

// secretRunDismissal is how a card dismissed at now ended: before its deadline (expires) the user closed it on the
// phone ("hand back to computer", or a client that only closes it); at its deadline it expired. Nothing ran either way.
func secretRunDismissal(now, expires time.Time) (status, message string) {
	if now.Before(expires.Add(-secretRunExpirySlack)) {
		return secretDeclined, "The user closed the password card on the phone without running it: nothing ran."
	}
	return secretExpired, fmt.Sprintf("Nobody filled in the password card within %d minutes: it expired and nothing ran.",
		int(secretRunCardWait.Minutes()))
}

// runSecret has machine's node run commands with password. The wait is not detached from the tool call: the node is
// not told when this call stops waiting (secret.run sends no rpc cancel) and stops the commands at the run's timeout
// itself, while giving up with the call drops this side's reference to the password at once.
func (s *Service) runSecret(ctx context.Context, machine string, commands []string, password string, timeout time.Duration) (proto.SecretRunAnswer, error) {
	pw := []byte(password)
	defer clear(pw)
	rctx, cancel := context.WithTimeout(ctx, timeout+secretRunMargin)
	defer cancel()
	var ans proto.SecretRunAnswer
	err := s.call(rctx, machine, proto.MethodSecretRun, proto.SecretRunParams{Commands: commands, Password: pw,
		TimeoutMS: timeout.Milliseconds()}, &ans)
	return ans, err
}

// checkSecretRunArgs checks a call before anything is asked, and returns the run's timeout.
func checkSecretRunArgs(a secretRunArgs) (time.Duration, error) {
	if a.Delivery != "" && a.Delivery != "stdin" {
		return 0, fmt.Errorf("delivery %q: only stdin is supported", a.Delivery)
	}
	if strings.TrimSpace(a.Machine) == "" { // machineByName would take "" for the first computer without an ssh alias
		return 0, errors.New("machine must name the computer to run on: its id or name")
	}
	if a.Purpose == "" || !proto.SecretRunPurposeShows(a.Purpose) || utf8.RuneCountInString(a.Purpose) > secretRunMaxPurpose {
		return 0, fmt.Errorf("purpose must be one line of at most %d visible characters (no control, zero-width, "+
			"bidirectional or line-separator characters) saying why the password is needed", secretRunMaxPurpose)
	}
	if len(a.Commands) == 0 || len(a.Commands) > proto.SecretRunMaxCommands {
		return 0, fmt.Errorf("commands must have 1 to %d commands, not %d", proto.SecretRunMaxCommands, len(a.Commands))
	}
	for i, c := range a.Commands {
		if why := proto.SecretRunCommandProblem(c); why != "" {
			return 0, fmt.Errorf("command %d cannot go on the card: %s", i+1, why)
		}
		switch sudo := proto.SecretRunSudoOf(c); {
		case sudo.WithoutStdin:
			return 0, fmt.Errorf("command %d calls sudo without -S: sudo would look for a terminal the computer does not have "+
				"and the password would be wasted. Write it as sudo -S -k -p '' …", i+1)
		case sudo.Calls > 1:
			return 0, fmt.Errorf("command %d calls sudo %d times, and its stdin has the password once: put each sudo in a "+
				"command of its own", i+1, sudo.Calls)
		case sudo.AfterPipe:
			return 0, fmt.Errorf("command %d runs sudo after a pipe, so sudo would read the pipe, not the password: run sudo "+
				"first (sudo -S -k -p '' sh -c '…' to run a pipeline as root)", i+1)
		}
	}
	// Checked in seconds: a large count times time.Second would overflow into a short duration.
	if a.TimeoutSeconds < 0 || a.TimeoutSeconds > int(proto.SecretRunMaxTimeout/time.Second) {
		return 0, fmt.Errorf("timeout_seconds must be 1 to %d", int(proto.SecretRunMaxTimeout/time.Second))
	}
	if a.TimeoutSeconds == 0 {
		return secretRunDefaultTimeout, nil
	}
	return time.Duration(a.TimeoutSeconds) * time.Second, nil
}

// secretRunMachineLabel is how the card names m: its name, with its id when another computer of this manager has the
// same name (any case) or that name for its id or ssh alias, so the user can tell which one gets the password.
func secretRunMachineLabel(m Device, all []Device) string {
	for _, d := range all {
		if d.ID != m.ID && (strings.EqualFold(d.Name, m.Name) || strings.EqualFold(d.ID, m.Name) ||
			d.SSHHost != "" && strings.EqualFold(d.SSHHost, m.Name)) {
			return m.Name + " (" + m.ID + ")"
		}
	}
	return m.Name
}

// secretRunSummary is the card's text, for a client that does not know secret_run: the computer, the purpose and
// every command verbatim, one per line after "$ ".
func secretRunSummary(l i18n.Lang, machine, purpose string, commands []string) string {
	var b strings.Builder
	b.WriteString(i18n.T(l, "assistant-secret_run.summary", i18n.Args{"machine": machine, "purpose": purpose}))
	for _, c := range commands {
		b.WriteString("\n$ ")
		b.WriteString(c)
	}
	return b.String()
}

// secretRunResults are the node's results as the Agent gets them: the commands the user approved (not the node's echo),
// each output clipped to its end, and none of an output that held the password (the node withholds it; this is the
// check behind it).
func secretRunResults(commands []string, results []proto.SecretRunResult, password string) []proto.SecretRunResult {
	results = results[:min(len(results), len(commands))]
	out := make([]proto.SecretRunResult, len(results))
	for i, r := range results {
		r.Command = commands[i]
		if r.OutputWithheld || strings.Contains(r.Output, password) {
			r.Output, r.Truncated, r.OutputWithheld = "", false, true
		}
		if runes := []rune(r.Output); len(runes) > secretRunOutputRunes {
			r.Output, r.Truncated = string(runes[len(runes)-secretRunOutputRunes:]), true
		}
		out[i] = r
	}
	return out
}

// secretRunVerdict is a run's status and message from its results.
func secretRunVerdict(machine string, total int, results []proto.SecretRunResult, timeout time.Duration) (string, string) {
	status, message := secretRunOutcomeOf(machine, total, results, timeout)
	var withheld []string
	for i, r := range results {
		if r.OutputWithheld {
			withheld = append(withheld, fmt.Sprint(i+1))
		}
	}
	if len(withheld) > 0 {
		message += fmt.Sprintf(" The output of command %s was withheld: it contained the password, and where a masked "+
			"password sits could give it away.", strings.Join(withheld, ", "))
	}
	return status, message
}

func secretRunOutcomeOf(machine string, total int, results []proto.SecretRunResult, timeout time.Duration) (string, string) {
	const used = " " + secretRunUsed
	for i, r := range results {
		switch {
		case r.TimedOut:
			return secretFailed, fmt.Sprintf("Command %d of %d on %s ran past the run's %s and was stopped (SIGTERM, then "+
				"SIGKILL), but what sudo started as root may still be running: check its state on the computer before "+
				"asking for a new card. The commands after it did not run.%s", i+1, total, machine, timeout, used)
		case r.ExitCode < 0:
			return secretFailed, fmt.Sprintf("Command %d of %d could not run to its end on %s; the commands after it did not run.%s",
				i+1, total, machine, used)
		case r.ExitCode != 0:
			return secretFailed, fmt.Sprintf("Command %d of %d failed on %s (exit code %d); the commands after it did not run.%s",
				i+1, total, machine, r.ExitCode, used)
		}
	}
	if len(results) < total {
		return secretFailed, fmt.Sprintf("Only %d of %d commands ran on %s.%s", len(results), total, machine, used)
	}
	noun := "commands"
	if total == 1 {
		noun = "command"
	}
	return secretRan, fmt.Sprintf("Ran %d %s on %s with the password the user typed; the password was used once and discarded.", total, noun, machine)
}
