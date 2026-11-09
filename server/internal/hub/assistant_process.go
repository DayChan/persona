package hub

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/identity"
)

// The assistant runs as its own process, `personad assistant` (internal/assistant), on the hub's bus. Unless hub.json
// says assistant.external, the hub starts it beside itself, from the manager's program beside its own binary
// (managerBinary), with a fresh bus token on its stdin (never on the command line; it stops when its stdin closes),
// and starts it again when it exits: 1 s doubling to a minute, from 1 s again once it ran a minute. So a hub's systemd
// unit, its Docker image and deploy.sh stay as they are. An assistant run elsewhere gets its token from
// `personad hub assistant-token` (docs/assistant/assistant.md "Process").

// assistantCommand is the assistant's command line (tests run another): `<managerBinary> assistant`.
var assistantCommand = func(ctx context.Context) (*exec.Cmd, error) {
	self, err := runningBinary()
	if err != nil {
		return nil, err
	}
	exe, note, err := managerBinary(ctx, self, programVersion)
	if err != nil {
		return nil, err
	}
	if note != "" {
		log.Printf("assistant: %s", note)
	}
	return exec.CommandContext(ctx, exe, "assistant"), nil
}

// managerBinary is the program the hub starts the built-in manager with, for a hub running from self: self when that
// is personad, which runs everything (docs/server/server.md "Programs"); for persona-hub, which has no manager in it,
// the persona-manager beside it when it is the same build (what version says `version -v` prints), else the personad
// beside it (ServerBinary). note says why a persona-manager there was passed over.
func managerBinary(ctx context.Context, self string, version func(context.Context, string) (string, error)) (bin, note string, err error) {
	if !isProgram(self, "persona-hub") {
		return self, "", nil
	}
	pm := filepath.Join(filepath.Dir(self), "persona-manager"+filepath.Ext(self))
	if _, statErr := os.Stat(pm); statErr == nil {
		mine, err := version(ctx, self)
		theirs, perr := version(ctx, pm)
		switch {
		case err == nil && perr == nil && mine == theirs:
			return pm, "", nil
		case perr != nil:
			note = fmt.Sprintf("%s does not run (%v)", pm, perr)
		case err != nil:
			note = fmt.Sprintf("this hub's own version -v failed (%v)", err)
		default:
			note = fmt.Sprintf("%s is another build (%q, the hub %q)", pm, firstLine(theirs), firstLine(mine))
		}
	}
	bin, err = serverBinaryOf(self)
	if err != nil {
		return "", "", err
	}
	if note != "" {
		note += ": starting the built-in manager with " + bin
	}
	return bin, note, nil
}

// programVersion is what the binary at path says to `version -v`.
func programVersion(ctx context.Context, path string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, path, "version", "-v").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return line
}

// superviseAssistant keeps the assistant's process running until ctx ends.
func (h *Hub) superviseAssistant(ctx context.Context) {
	if h.cfg.Assistant.External {
		h.logf("assistant: hub.json says it runs elsewhere (assistant.external): not starting one")
		return
	}
	if !h.cfg.Assistant.StartsBuiltin() {
		if h.cfg.Assistant.ServiceBound() {
			h.logf("assistant: hub.json says it is a service of its own (assistant.mode service): not starting one")
		} else { // a mode this version does not know: none rather than one next to whatever that mode runs
			h.logf("assistant: hub.json's assistant.mode %q is not one this version knows (child, service): not starting one", h.cfg.Assistant.Mode)
		}
		return
	}
	token, err := h.createBusToken(ctx, assistantwire.AssistantID, "助手")
	if err != nil {
		h.logf("assistant: no bus token, not starting it: %v", err)
		return
	}
	first, most := time.Second, time.Minute
	delay := first
	for ctx.Err() == nil {
		began := time.Now()
		err := h.runAssistant(ctx, token)
		if ctx.Err() != nil {
			return
		}
		if time.Since(began) >= time.Minute {
			delay = first
		}
		h.logf("assistant: exited (%v); starting it again in %s", err, delay)
		select {
		case <-ctx.Done():
			return
		case <-time.After(delay):
		}
		delay = min(delay*2, most)
	}
}

// runAssistant runs the assistant's process once, until it exits or ctx ends (then it is asked to stop: SIGTERM,
// killed 10 s later). Its stdin stays open while this process lives: it stops when it closes, so a hub that died
// leaves no assistant behind.
func (h *Hub) runAssistant(ctx context.Context, token string) error {
	cmd, err := assistantCommand(ctx)
	if err != nil {
		return err
	}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return err
	}
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	cmd.Env = identity.Environment(os.Environ(), "PERSONA_ASSISTANT_HUB="+hubLocalURL(h.cfg.Listen),
		"PERSONA_ASSISTANT_DEPLOY=child")
	cmd.Cancel = func() error { return cmd.Process.Signal(syscall.SIGTERM) }
	cmd.WaitDelay = 10 * time.Second
	if err := cmd.Start(); err != nil {
		return err
	}
	_, _ = io.WriteString(stdin, token+"\n")
	return cmd.Wait() // closes stdin
}

// AssistantToken is `personad hub assistant-token`: a new bus token for an assistant run elsewhere (hub.json
// assistant.external), printed once. A connection with the token it replaces is hung up.
func AssistantToken(ctx context.Context, out io.Writer) error {
	cfg, err := runningHubConfig()
	if err != nil {
		return err
	}
	if cfg.Assistant.ServiceBound() { // the token would replace the bound service's
		return errors.New("the built-in manager is a service of its own here (assistant.mode service): bind it with " +
			"manager-code and persona-manager join --builtin; assistant-token is for assistant.external")
	}
	if cfg.DatabaseURL == "" {
		return errors.New("hub.json has no database_url")
	}
	database, err := db.Open(ctx, cfg.DatabaseURL, db.HubSet)
	if err != nil {
		return err
	}
	defer database.Close()
	h := &Hub{cfg: cfg, store: database, logger: log.New(io.Discard, "", 0)}
	tok, err := h.createBusToken(ctx, assistantwire.AssistantID, "助手")
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "%s\n\n在跑助手的机器上：PERSONA_ASSISTANT_TOKEN=<上面的 token> PERSONA_ASSISTANT_HUB=<hub 地址> personad assistant\n"+
		"（hub.json 的 assistant.external 要是 true，否则 hub 每次启动都换掉这个 token、自己起一个助手）\n", tok)
	return err
}

// hubLocalURL is the hub on this machine, from its listen address.
func hubLocalURL(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil {
		return "http://127.0.0.1:18660"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
