// Package toolcli is `personad tool`: a connector's tools from the assistant's shell, through the node on this machine
// (node/clicall.go lets only the assistant's processes through and only the tools of the connectors signed in), which
// asks the hub, whose gate decides each write (connector_gate: by default the user allows it on the phone).
//
//	personad tool <tool> [<json args> | -]
//	    one call; its answer on stdout
//	personad tool --cli <connector> [--format json|text] <words…>
//	    the connector's command line (Telegram's tg, on the assistant's PATH: node/clicall.go): the connector's
//	    <connector>_cli tool reads the words and names the tool to call; that tool is called (through the gate, as by
//	    name); with --format text the connector shows its answer for people (docs/protocol/connector-protocol.md "Command line")
//
// Output is for a program: one line of JSON on stdout, or text for a person. A failure is one line of JSON on stderr
// ({error, kind, retry_after}), or "<name>: …" with --format text, and an exit code by its kind; a mistake in the
// command line is one too (kind usage), found before anything is called.
package toolcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/nodeclient"
	"github.com/nwverse/persona/server/internal/proto"
)

// Exit codes, by what went wrong (the same as Telegram's tg had).
const (
	exitOK          = 0
	exitFailed      = 1 // the call failed (error says why)
	exitUsage       = 2 // the command line is wrong
	exitUnavailable = 3 // the node, the hub or the connector cannot be reached or is not signed in
	exitRateLimited = 4 // the connector waits out a rate limit (retry_after seconds)
	exitDeclined    = 5 // the user (or connector_gate) did not allow the write
	// exitInterrupted: Ctrl-C / SIGTERM (as personad ask): the node hung up and the card came off the phone; nothing
	// is wrong with the node or the hub.
	exitInterrupted = 130
)

var kinds = map[int]string{exitFailed: "failed", exitUsage: "usage", exitUnavailable: "unavailable", exitRateLimited: "rate_limited",
	exitDeclined: "declined", exitInterrupted: "interrupted"}

// callTimeout covers a write waiting for the user on the phone (the card lasts 15 minutes).
const callTimeout = 30 * time.Minute

// stdinMost is the most a command line reads from stdin (a message's text).
const stdinMost = 1 << 20

// readStdin reads all of stdin, failing when it holds more than stdinMost bytes instead of cutting it short: a
// truncated message would be sent as if it were whole.
func readStdin(stdin io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(stdin, stdinMost+1))
	if err != nil {
		return nil, fmt.Errorf("stdin: %w", err)
	}
	if len(b) > stdinMost {
		return nil, fmt.Errorf("stdin: more than %d bytes (1 MiB), nothing was sent", stdinMost)
	}
	return b, nil
}

// dial reaches the node; it hangs up when ctx is cancelled (Ctrl-C, SIGTERM), so a write waiting for the user ends on
// the node too and the hub takes its card off the phone (rpc.Peer.Cancels), instead of a card nobody waits for until
// it expires (replaced by tests).
var dial nodeclient.Dialer = nodeclient.DialInterruptible

// call asks the node to run a tool as the assistant's (replaced by tests).
var call = func(ctx context.Context, name string, args any) (proto.ToolResult, error) {
	line, err := dial(ctx, map[string]any{"kind": "cli.call", "name": name, "args": args})
	if err != nil {
		return proto.ToolResult{}, err
	}
	var res proto.ToolResult
	if err := json.Unmarshal(line, &res); err != nil {
		return proto.ToolResult{}, fmt.Errorf("bad answer from the Persona node: %w", err)
	}
	return res, nil
}

const (
	usageTool = "personad tool <tool> [<json args> | -]"
	usageCLI  = "personad tool --cli <connector> [--format json|text] <words…>"
	usage     = "personad tool: a connector's tools from the assistant's shell, through the Persona hub\n  " + usageTool + "\n  " + usageCLI
)

// Main runs one command line and returns its exit code. Help is the usage on stdout; a mistake in the command line
// is a failure of kind usage on stderr, before anything is called.
func Main(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help" || args[0] == "help") {
		fmt.Fprintln(stdout, usage)
		return exitOK
	}
	ctx, cancel := context.WithTimeout(ctx, callTimeout)
	defer cancel()
	if len(args) > 0 && args[0] == "--cli" {
		return commandLine(ctx, args[1:], stdin, stdout, stderr)
	}
	out := failer{name: "personad tool", stderr: stderr}
	var toolArgs json.RawMessage = json.RawMessage(`{}`)
	switch {
	case len(args) == 0 || args[0] == "":
		return out.fail(exitUsage, "no tool named: "+usageTool+", or "+usageCLI, 0)
	case strings.HasPrefix(args[0], "-"):
		return out.fail(exitUsage, "unknown option "+args[0]+": "+usageTool+", or "+usageCLI, 0)
	case len(args) > 2:
		return out.fail(exitUsage, "one tool and one JSON object of its arguments", 0)
	case len(args) == 2 && args[1] == "-":
		b, err := readStdin(stdin)
		if err != nil {
			return out.fail(exitUsage, err.Error(), 0)
		}
		toolArgs = b
	case len(args) == 2:
		toolArgs = json.RawMessage(args[1])
	}
	if mistake := argsMistake(toolArgs); mistake != "" {
		return out.fail(exitUsage, mistake, 0)
	}
	res, err := call(ctx, args[0], toolArgs)
	if err != nil {
		return out.unreachable(ctx, err)
	}
	text := firstText(res)
	if res.IsError {
		code, retry := classify(res)
		return out.fail(code, text, retry)
	}
	fmt.Fprintln(stdout, text)
	return exitOK
}

// argsMistake is what is wrong with a tool's arguments, which are one JSON object ("" when nothing is).
func argsMistake(b []byte) string {
	if !json.Valid(b) {
		return "the arguments are not JSON"
	}
	if t := bytes.TrimLeft(b, " \t\r\n"); len(t) == 0 || t[0] != '{' {
		return "the arguments are not a JSON object: " + usageTool
	}
	return ""
}

// plan is what a connector's <id>_cli tool makes of a command line: the call it means, or what to say right away
// (its usage, a mistake in the words) with an exit code.
type plan struct {
	Name   string `json:"name"` // the command, for messages ("tg")
	Exit   *int   `json:"exit"`
	Stdout string `json:"stdout"`
	Stderr string `json:"stderr"`
	Call   *struct {
		Tool string          `json:"tool"`
		Args json.RawMessage `json:"args"`
	} `json:"call"`
	Render string `json:"render"` // how --format text asks the connector to show the answer ("" prints it as it is)
}

// commandLine runs the connector's command line (`<connector> [--format json|text] <words…>`): the words planned by
// <connector>_cli, the tool it names called, the answer shown.
func commandLine(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	conn := ""
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		conn, args = args[0], args[1:]
	}
	format, words, mistake := formatFlag(args)
	out := failer{name: conn, text: format == "text", stderr: stderr}
	switch {
	case conn == "":
		out.name = "personad tool"
		return out.fail(exitUsage, "no connector named: "+usageCLI, 0)
	case mistake != "":
		return out.fail(exitUsage, mistake, 0)
	case format != "json" && format != "text":
		return out.fail(exitUsage, fmt.Sprintf("--format %s: json or text", format), 0)
	}
	in := map[string]any{"argv": append([]string{}, words...)}
	if slices.Contains(words, "-") { // the text comes from stdin (tg send <chat> -)
		b, err := readStdin(stdin)
		if err != nil {
			return out.fail(exitUsage, err.Error(), 0)
		}
		in["stdin"] = string(b)
	}
	res, err := call(ctx, conn+"_cli", in)
	if err != nil {
		return out.unreachable(ctx, err)
	}
	if res.IsError {
		code, retry := classify(res)
		return out.fail(code, firstText(res), retry)
	}
	var p plan
	if err := json.Unmarshal([]byte(firstText(res)), &p); err != nil {
		return out.fail(exitFailed, conn+"_cli's answer is not a plan: "+err.Error(), 0)
	}
	if p.Name != "" {
		out.name = p.Name
	}
	if p.Exit != nil {
		if p.Stdout != "" {
			fmt.Fprintln(stdout, strings.TrimRight(p.Stdout, "\n"))
		}
		if p.Stderr != "" {
			if *p.Exit == exitOK {
				fmt.Fprintln(stderr, strings.TrimRight(p.Stderr, "\n"))
			} else {
				return out.fail(*p.Exit, p.Stderr, 0)
			}
		}
		return *p.Exit
	}
	if p.Call == nil || p.Call.Tool == "" {
		return out.fail(exitFailed, conn+"_cli named no tool to call", 0)
	}
	toolArgs := p.Call.Args
	if len(toolArgs) == 0 {
		toolArgs = json.RawMessage(`{}`)
	}
	res, err = call(ctx, p.Call.Tool, toolArgs)
	if err != nil {
		return out.unreachable(ctx, err)
	}
	text := firstText(res)
	if res.IsError {
		code, retry := classify(res)
		return out.fail(code, text, retry)
	}
	if format != "text" || p.Render == "" {
		fmt.Fprintln(stdout, text)
		return exitOK
	}
	shown, err := call(ctx, conn+"_cli", map[string]any{"render": p.Render, "result": text})
	if err != nil || shown.IsError {
		why := firstText(shown)
		if err != nil {
			why = err.Error()
		}
		return out.fail(exitFailed, "the answer could not be shown as text: "+why, 0)
	}
	var page struct {
		Stdout string `json:"stdout"`
	}
	if err := json.Unmarshal([]byte(firstText(shown)), &page); err != nil {
		return out.fail(exitFailed, "the answer could not be shown as text: "+err.Error(), 0)
	}
	fmt.Fprint(stdout, page.Stdout)
	if !strings.HasSuffix(page.Stdout, "\n") && page.Stdout != "" {
		fmt.Fprintln(stdout)
	}
	return exitOK
}

// formatFlag takes --format json|text (or --format=…) out of a command line's words; a --format with nothing after it
// is a mistake, not a word for the connector.
func formatFlag(args []string) (format string, words []string, mistake string) {
	format = "json"
	for i := 0; i < len(args); i++ {
		switch a := args[i]; {
		case a == "--format" && i+1 < len(args):
			format = args[i+1]
			i++
		case a == "--format":
			mistake = "--format needs a value: json or text"
		case strings.HasPrefix(a, "--format="):
			format = strings.TrimPrefix(a, "--format=")
		default:
			words = append(words, a)
		}
	}
	return format, words, mistake
}

// failer writes a failure as the format says: "<name>: …" for people, one line of JSON for a program.
type failer struct {
	name   string
	text   bool
	stderr io.Writer
}

func (f failer) fail(code int, msg string, retry int) int {
	if f.text {
		fmt.Fprintln(f.stderr, f.name+": "+msg)
		return code
	}
	out := map[string]any{"error": msg, "kind": kinds[code]}
	if retry > 0 {
		out["retry_after"] = retry
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b) // one line, with its newline
	enc.SetEscapeHTML(false)   // the usage's <tool> stays readable as it is, not \u003ctool\u003e
	_ = enc.Encode(out)
	_, _ = f.stderr.Write(b.Bytes())
	return code
}

// unreachable is the failure of a call that did not come back: interrupted when ctx ended first (the caller stopped
// it, so the node and the hub are fine), else the node could not be reached.
func (f failer) unreachable(ctx context.Context, err error) int {
	if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return f.fail(exitInterrupted, "interrupted: the call was stopped before its result came back (a write still waiting for the user was withdrawn; one already approved may have run, so check before retrying)", 0)
	}
	return f.fail(exitUnavailable, fmt.Sprintf("the Persona node on this machine is not reachable (%v): is the hub connected?", err), 0)
}

func firstText(res proto.ToolResult) string {
	if len(res.Content) > 0 {
		return res.Content[0].Text
	}
	return ""
}

// byKind is the exit code of each kind of failure the hub, the node and the connectors name (proto.ToolError*).
var byKind = map[string]int{proto.ToolErrorDeclined: exitDeclined, proto.ToolErrorUnavailable: exitUnavailable,
	proto.ToolErrorRateLimited: exitRateLimited}

// waitFor is how the hub and the connectors say how long a rate limit lasts, for those that do not say it in
// retry_after (an older hub, a connector's status).
var waitFor = lazyre.New(`(?:还要 |限流中?，)(\d+) 秒`)

// classify is a failure's exit code and how long a rate limit lasts: by the kind the hub, the node or the connector
// named (error_kind, retry_after), or, when none is named (an older hub or node), by what the error says.
func classify(res proto.ToolResult) (int, int) {
	text := firstText(res)
	if code, ok := byKind[res.ErrorKind]; ok {
		retry := res.RetryAfter
		if m := waitFor.FindStringSubmatch(text); code == exitRateLimited && retry <= 0 && m != nil {
			retry, _ = strconv.Atoi(m[1])
		}
		return code, max(retry, 0)
	}
	return guess(text)
}

// guess is a failure's exit code by what the error says, from a hub or node that names no kind.
func guess(text string) (int, int) {
	if m := waitFor.FindStringSubmatch(text); m != nil {
		n, _ := strconv.Atoi(m[1])
		return exitRateLimited, n
	}
	switch {
	case strings.Contains(text, "is not available"), strings.Contains(text, "只能在助手的对话里用"), strings.HasPrefix(text, "Persona hub error"):
		return exitUnavailable, 0
	case strings.HasPrefix(text, "The user declined"), strings.Contains(text, "(connector_gate) do not allow"):
		return exitDeclined, 0
	}
	return exitFailed, 0
}
