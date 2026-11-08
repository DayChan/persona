// Package supervise is `personad supervise`: the hub and the built-in manager as two processes side by side in one
// container (docs/operations/docker.md "The hub and the built-in manager in one container"). Each is started again
// when it stops, after a pause that grows while it keeps stopping, and the other is left running. With
// hub.json's assistant.mode service the built-in manager is a service of its own (docs/assistant/managers.md "The
// built-in manager as its own service"): the supervisor binds it to the hub (a one-time code made from the hub's
// database, traded over loopback) before it starts it. Otherwise the hub starts its own child as it always has, and
// the supervisor becomes the hub (exec): the container is what it was. With --manager-only it runs the built-in
// manager alone, for a container of its own beside the hub's (compose.yaml's commented example).
package supervise

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/hub"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/manager"
)

// SeparateEnv on the hub's container says the built-in manager runs in a container of its own (--manager-only):
// the hub's then runs the hub alone.
const SeparateEnv = "PERSONA_BUILTIN_MANAGER"

// Usage is the command's help.
const Usage = `usage: personad supervise [--manager-only [--hub <hub address>]]
  the hub and, with hub.json's assistant.mode service, the built-in manager beside it, each started again when it
  stops (the hub image runs this); --manager-only: the built-in manager alone, bound to the hub at --hub
  (default http://hub:18660, the hub's compose service)`

var (
	// backoffFirst is the first pause before a program that stopped starts again; it doubles up to backoffMost while
	// the program keeps stopping within steadyAfter of its start.
	backoffFirst = time.Second
	backoffMost  = 30 * time.Second
	steadyAfter  = time.Minute
	// stopWait is how long the programs get to stop after a signal, before they are killed: the hub's own shutdown
	// budget and a second more, so that the hub is not cut short, yet under Docker's own stop timeout (10 s unless
	// compose's stop_grace_period says otherwise), so that the supervisor is the one that kills. hub.json's
	// shutdown_timeout_seconds makes it that and a second (hubFile.stopWait).
	stopWait = hub.DefaultShutdownTimeout + time.Second
	// healthWait bounds one wait for the hub to answer before a binding is tried; bindRetry is the first pause after
	// a binding that failed, doubling up to bindRetryMost while it keeps failing (each try opens the hub's database).
	healthWait    = 2 * time.Minute
	bindRetry     = 10 * time.Second
	bindRetryMost = 5 * time.Minute
	// voidWait bounds voiding a code that was not traded: short, as a stop may wait for it (the code runs out by
	// itself anyway).
	voidWait = 3 * time.Second
	// modeRecheck is how often --manager-only reads hub.json again while the built-in manager is not a service.
	modeRecheck = 30 * time.Second
)

// env is what the supervisor touches outside itself, so that tests stand in for it.
type env struct {
	self string // the program to run the two with
	home string // the data directory
	out  io.Writer
	// execHub replaces this process with the hub (the container as it was).
	execHub func(self string, env []string) error
	// bind makes sure the built-in manager is bound to the hub at hubURL (bind, below).
	bind    func(ctx context.Context, hubURL string) error
	environ []string
	// stop is how long the programs get to stop after a signal (stopWait unless hub.json says otherwise).
	stop time.Duration
}

// Run is `personad supervise [--manager-only [--hub <url>]]`.
func Run(ctx context.Context, args []string, out io.Writer) error {
	fs := flag.NewFlagSet("supervise", flag.ContinueOnError)
	fs.SetOutput(out)
	only := fs.Bool("manager-only", false, "the built-in manager alone, in a container of its own")
	hubURL := fs.String("hub", "http://hub:18660", "with --manager-only: the hub's address")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return clitext.UsageError(Usage)
	}
	if fs.NArg() != 0 {
		return clitext.UsageError(Usage)
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	e := &env{self: self, home: identity.HomeDir(), out: out, execHub: execHub, environ: os.Environ()}
	e.bind = e.bindBuiltin
	if *only {
		return e.managerOnly(ctx, strings.TrimRight(*hubURL, "/"))
	}
	hubSet := false
	fs.Visit(func(f *flag.Flag) { hubSet = hubSet || f.Name == "hub" })
	if hubSet {
		e.logf("--hub goes with --manager-only only: ignored")
	}
	return e.hubContainer(ctx)
}

func (e *env) logf(format string, args ...any) {
	fmt.Fprintf(e.out, "supervise: "+format+"\n", args...)
}

// hubFile is what the supervisor reads of hub.json (never writing it: the hub does).
type hubFile struct {
	Listen          string `json:"listen"`
	ShutdownTimeout int    `json:"shutdown_timeout_seconds"`
	Assistant       struct {
		Mode     string `json:"mode"`
		External bool   `json:"external"`
	} `json:"assistant"`
}

// readHubFile reads hub.json in the data directory; a missing one is a hub's first start: its defaults.
func (e *env) readHubFile() (hubFile, error) {
	var f hubFile
	b, err := os.ReadFile(filepath.Join(e.home, "hub.json"))
	if err != nil && !os.IsNotExist(err) {
		return f, err
	}
	if err == nil {
		if err := json.Unmarshal(b, &f); err != nil {
			return f, fmt.Errorf("hub.json: %w", err)
		}
	}
	if v := identity.Getenv("PERSONA_LISTEN"); v != "" { // as the hub takes it (hub.Config.applyEnv)
		f.Listen = v
	}
	return f, nil
}

// serviceMode: the built-in manager is a service of its own here (hub.Config's ServiceBound).
func (f hubFile) serviceMode() bool {
	return !f.Assistant.External && f.Assistant.Mode == hub.AssistantModeService
}

// stopWait is how long the programs get to stop after a signal with this hub.json: the hub's shutdown budget and a
// second more.
func (f hubFile) stopWait() time.Duration {
	if f.ShutdownTimeout > 0 {
		return time.Duration(f.ShutdownTimeout)*time.Second + time.Second
	}
	return stopWait
}

// loopback is the hub's address on this machine for its listen address (hub's hubLocalURL): loopback when it
// listens on every address, else the address it listens on.
func loopback(listen string) string {
	host, port, err := net.SplitHostPort(listen)
	if err != nil || port == "" {
		return "http://127.0.0.1:18660"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}

// hubContainer is the hub's container: the hub alone (exec) unless the built-in manager is a service of its own here;
// then the two side by side (the manager not, when SeparateEnv says it has a container of its own).
func (e *env) hubContainer(ctx context.Context) error {
	f, err := e.readHubFile()
	if err != nil {
		return err
	}
	if !f.serviceMode() {
		e.logf("assistant.mode is not service: the hub starts the built-in manager itself, as before")
		return e.execHub(e.self, e.environ)
	}
	e.stop = f.stopWait()
	// The hub reads hub.json when it starts: one that starts again with another mode (manager-mode, then an admin's
	// restart of the hub) needs the supervisor to decide again too. It stops then, and the container starts it again.
	progs := []*program{{name: "hub", args: []string{"hub", "run"}, after: func() error {
		now, err := e.readHubFile()
		if err != nil {
			return fmt.Errorf("%w: %w", errUnreadable, err)
		}
		if now.serviceMode() != f.serviceMode() || loopback(now.Listen) != loopback(f.Listen) {
			return errChanged
		}
		return nil
	}}}
	switch v := identity.Getenv(SeparateEnv); v {
	case "separate":
		e.logf("%s=separate: the built-in manager runs in a container of its own; here, the hub alone", SeparateEnv)
	default:
		if v != "" {
			e.logf("%s=%s is not separate: ignored, the built-in manager runs here", SeparateEnv, v)
		}
		hubURL := loopback(f.Listen)
		progs = append(progs, &program{name: "manager", args: []string{"assistant"},
			env:    []string{"PERSONA_ASSISTANT_DEPLOY=" + assistant.DeployService},
			before: func(ctx context.Context) error { return e.bind(ctx, hubURL) }})
	}
	return e.supervise(ctx, progs)
}

// managerOnly is the built-in manager's own container: the manager, bound to the hub at hubURL, each time hub.json
// says assistant.mode service (the hub starts its own child otherwise). hub.json is read again before each start:
// a hub gone back to its child since hands the child a new token, which stops this one, and then it waits.
func (e *env) managerOnly(ctx context.Context, hubURL string) error {
	return e.supervise(ctx, []*program{{name: "manager", args: []string{"assistant"},
		env: []string{"PERSONA_ASSISTANT_DEPLOY=" + assistant.DeployService},
		before: func(ctx context.Context) error {
			if !e.waitService(ctx) {
				return ctx.Err()
			}
			return e.bind(ctx, hubURL)
		}}})
}

// waitService waits until hub.json says the built-in manager is a service of its own, reading it again every
// modeRecheck and saying once why it waits; false when ctx ended first.
func (e *env) waitService(ctx context.Context) bool {
	said := false
	for {
		f, err := e.readHubFile()
		if err == nil && f.serviceMode() {
			return true
		}
		if !said {
			why := "assistant.mode is not service: the hub starts the built-in manager itself"
			if err != nil {
				why = err.Error()
			}
			e.logf("%s; waiting (hub.json is read again every %s)", why, modeRecheck)
			said = true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(modeRecheck):
		}
	}
}

// program is one of the programs supervised.
type program struct {
	name string
	args []string
	env  []string
	// before runs before each start; an error is said, and it is tried again after bindRetry (growing).
	before func(ctx context.Context) error
	// after runs each time it stopped: an error stops the supervisor (errChanged: the container is to start again).
	after func() error
}

var (
	// ErrStopped: the supervisor stopped the programs for a reason it said in its log (errChanged, errUnreadable), so
	// that the container starts again; personad exits 1 without saying it twice.
	ErrStopped = errors.New("supervise: stopped")
	// errChanged: hub.json says another mode (or listen address) than the supervisor started with.
	errChanged = errors.New("hub.json's assistant.mode or listen address changed: stopping so that the container " +
		"starts again with it")
	// errUnreadable: hub.json can't be read once the hub stopped (the hub would not start on it either).
	errUnreadable = errors.New("hub.json can't be read after the hub stopped: stopping so that the container starts " +
		"again with it")
)

// supervise runs progs, each started again when it stops, until ctx ends or one's after says to stop; then they are
// stopped (SIGTERM, then SIGKILL after e.stop) and it returns (ErrStopped with that error, said already).
func (e *env) supervise(ctx context.Context, progs []*program) error {
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var wg sync.WaitGroup
	for _, p := range progs {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := e.keep(ctx, p, len(progs) > 1); err != nil {
				e.logf("%v", err)
				cancel(err)
			}
		}()
	}
	wg.Wait()
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return fmt.Errorf("%w: %w", ErrStopped, err)
	}
	return nil
}

// keep runs p until ctx ends, again each time it stops; an error when p.after says to stop.
func (e *env) keep(ctx context.Context, p *program, others bool) error {
	pause, bindPause := backoffFirst, bindRetry
	for ctx.Err() == nil {
		if p.before != nil {
			if err := p.before(ctx); err != nil {
				if ctx.Err() != nil {
					return nil
				}
				e.logf("%s: %v; trying again in %s", p.name, err, bindPause)
				sleep(ctx, bindPause)
				bindPause = min(bindPause*2, bindRetryMost)
				continue
			}
			bindPause = bindRetry
		}
		started := time.Now()
		err := e.run(ctx, p)
		if ctx.Err() != nil {
			return nil
		}
		if p.after != nil {
			if err := p.after(); err != nil {
				return err
			}
		}
		if time.Since(started) >= steadyAfter {
			pause = backoffFirst
		}
		rest := ""
		if others {
			rest = "; the other runs on"
		}
		e.logf("%s stopped (%v): starting it again in %s%s", p.name, exitText(err), pause, rest)
		sleep(ctx, pause)
		pause = min(pause*2, backoffMost)
	}
	return nil
}

// run starts p and waits for it; once ctx ends it is asked to stop (SIGTERM), and killed after e.stop.
// Each runs in a session of its own: what is left in it when the program stopped is killed (killGroup), its
// extensions' programs too, which have process groups of their own, so that a program started again after a crash
// doesn't find the last one's still running.
func (e *env) run(ctx context.Context, p *program) error {
	cmd := exec.Command(e.self, p.args...)
	cmd.Env = append(append([]string{}, e.environ...), p.env...)
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	ownGroup(cmd)
	if err := cmd.Start(); err != nil {
		return err
	}
	defer killGroup(cmd)
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	wait := e.stop
	if wait <= 0 {
		wait = stopWait
	}
	select {
	case err := <-done:
		return err
	case <-time.After(wait):
		e.logf("%s did not stop within %s: killing it", p.name, wait)
		_ = cmd.Process.Kill()
		return <-done
	}
}

// exitText says how a program ended.
func exitText(err error) string {
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ProcessState.String()
	}
	if err == nil {
		return "exit status 0"
	}
	return err.Error()
}

func sleep(ctx context.Context, d time.Duration) {
	select {
	case <-ctx.Done():
	case <-time.After(d):
	}
}

// bindBuiltin waits until the hub at hubURL answers, then binds the built-in manager to it unless its binding works:
// a one-time code made in the hub's database (as `persona-hub manager-code` makes one), traded over hubURL, and
// voided after when it was not.
func (e *env) bindBuiltin(ctx context.Context, hubURL string) error {
	if err := waitHealthy(ctx, hubURL, healthWait); err != nil {
		return err
	}
	var minted string
	traded, err := bindBuiltin(ctx, e.home, hubURL, func(ctx context.Context) (string, error) {
		code, err := mintCode(ctx)
		minted = code
		return code, err
	})
	if minted != "" && !traded {
		vctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), voidWait)
		_ = voidCode(vctx, minted)
		cancel()
	}
	if err != nil {
		return fmt.Errorf("binding the built-in manager to the hub at %s: %w", hubURL, err)
	}
	if traded {
		e.logf("bound the built-in manager to the hub at %s", hubURL)
	}
	return nil
}

// What bindBuiltin binds with (tests: stand-ins): the hub's code from its database, the manager's binding.
var (
	mintCode    = hub.MintBuiltinCode
	voidCode    = hub.VoidBuiltinCode
	bindBuiltin = manager.BindBuiltin
)

// waitHealthy waits up to most for GET hubURL/api/v1/health to answer 200.
func waitHealthy(ctx context.Context, hubURL string, most time.Duration) error {
	u, err := url.JoinPath(hubURL, "/api/v1/health")
	if err != nil {
		return err
	}
	transport := &http.Transport{Proxy: nil}
	defer transport.CloseIdleConnections()
	client := &http.Client{Timeout: 5 * time.Second, Transport: transport}
	deadline := time.Now().Add(most)
	for {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
		if err != nil {
			return err
		}
		if resp, err := client.Do(req); err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if !time.Now().Before(deadline) {
			return fmt.Errorf("the hub at %s did not answer within %s", hubURL, most)
		}
		sleep(ctx, time.Second)
	}
}
