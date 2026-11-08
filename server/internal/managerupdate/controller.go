package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/update"
)

// Controller keeps operating-system effects replaceable in tests.
type Controller struct {
	GOOS         string
	UID          int
	Home         string
	Exe          string
	Build        update.Build
	Now          func() time.Time
	Command      func(context.Context, string, ...string) ([]byte, error)
	ReleaseCheck func(Plan, update.Build, string) error
	FileCheck    func(string, int) error
}

func New(build update.Build) (*Controller, error) {
	exe, e := os.Executable()
	if e != nil {
		return nil, e
	}
	home, e := os.UserHomeDir()
	if e != nil {
		return nil, e
	}
	return &Controller{GOOS: runtime.GOOS, UID: os.Getuid(), Home: home, Exe: exe, Build: build, Now: time.Now, ReleaseCheck: validateRelease, FileCheck: ownedIndependentFile, Command: func(ctx context.Context, n string, a ...string) ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		defer cancel()
		return exec.CommandContext(commandCtx, n, a...).CombinedOutput()
	}}, nil
}

// Main is deliberately an explicit subcommand and standard flag parser. No default apply exists.
func Main(ctx context.Context, build update.Build, args []string, out io.Writer) error {
	if len(args) == 0 {
		return errors.New("usage: personad manager update check|start|status|run|confirm|rollback|inventory --plan PATH")
	}
	action := args[0]
	switch action {
	case "check", "start", "status", "run", "confirm", "rollback", "inventory":
	default:
		return errors.New("unknown manager update action")
	}
	fs := flag.NewFlagSet("manager update "+action, flag.ContinueOnError)
	fs.SetOutput(out)
	path := fs.String("plan", "", "explicit release update plan")
	var mainID, machineID, machineUID *string
	if action == "inventory" {
		mainID = fs.String("main", "", "original persisted main session ID")
		machineID = fs.String("machine", "", "original machine ID")
		machineUID = fs.String("machine-uid", "", "original machine UID")
	}
	if e := fs.Parse(args[1:]); e != nil {
		if errors.Is(e, flag.ErrHelp) {
			return nil
		}
		return e
	}
	if *path == "" || fs.NArg() != 0 {
		return errors.New("--plan is required; positional arguments are not accepted")
	}
	p, e := ReadPlan(*path)
	if e != nil {
		return e
	}
	c, e := New(build)
	if e != nil {
		return e
	}
	if action == "inventory" {
		evidence, err := c.ReadIdleEvidence(ctx, p, *mainID, *machineID, *machineUID)
		return errors.Join(err, json.NewEncoder(out).Encode(evidence))
	}
	var r Receipt
	switch action {
	case "check":
		e = c.Check(ctx, p)
		r = receipt(p, "checked", e)
	case "status":
		r, e = readReceipt(p)
	case "start":
		e = c.Start(ctx, p)
		r = receipt(p, "checked", e)
	case "run":
		r, e = c.Run(ctx, p)
	case "confirm", "rollback":
		e = c.Acknowledge(ctx, p, action)
		r, e = acknowledgementReceipt(p, e)
	}
	if enc := json.NewEncoder(out).Encode(r); enc != nil {
		return errors.Join(e, enc)
	}
	return e
}

// Check reads files, build metadata and the loaded service. It creates no state or service.
func (c *Controller) Check(ctx context.Context, p Plan) error {
	if e := p.validateShape(); e != nil {
		return e
	}
	if c.GOOS != "darwin" {
		return errors.New("independent manager automatic activation currently requires Darwin launchd")
	}
	if c.UID != p.UID {
		return errors.New("updater UID differs from manager owner")
	}
	for _, path := range []string{p.Dir, p.Binary, p.PlistPath, c.Exe, filepath.Join(p.Dir, "manager.json")} {
		if e := canonical(path); e != nil {
			return e
		}
	}
	if c.FileCheck != nil {
		for _, path := range []string{p.Binary, p.PlistPath, filepath.Join(p.Dir, "manager.json")} {
			if e := c.FileCheck(path, p.UID); e != nil {
				return e
			}
		}
	}
	if p.PlistPath != filepath.Join(c.Home, "Library", "LaunchAgents", p.Unit+".plist") {
		return errors.New("plist is outside the current user's LaunchAgents")
	}
	if p.Unit != "com.persona.manager."+p.ManagerID && p.Unit != compat.LegacyManagerLabel(p.ManagerID) {
		return errors.New("unit is not this manager's original label")
	}
	for path, want := range map[string]string{filepath.Join(p.Dir, "manager.json"): p.ConfigSHA256, p.Binary: p.PreviousSHA256, p.PlistPath: p.PlistSHA256} {
		got, e := hashFile(path)
		if e != nil {
			return e
		}
		if got != want {
			return fmt.Errorf("identity hash mismatch: %s", filepath.Base(path))
		}
	}
	b, e := os.ReadFile(filepath.Join(p.Dir, "manager.json"))
	if e != nil {
		return e
	}
	var cfg struct {
		ID          string `json:"id"`
		DatabaseURL string `json:"database_url"`
	}
	if e = json.Unmarshal(b, &cfg); e != nil {
		return e
	}
	if cfg.ID != p.ManagerID || cfg.DatabaseURL == "" {
		return errors.New("manager identity or existing database missing")
	}
	if e = c.ReleaseCheck(p, c.Build, c.Exe); e != nil {
		return e
	}
	return c.checkUnit(ctx, p, true)
}
func (c *Controller) checkUnit(ctx context.Context, p Plan, live bool) error {
	b, e := c.Command(ctx, "/usr/bin/plutil", "-convert", "json", "-o", "-", p.PlistPath)
	if e != nil {
		return fmt.Errorf("read original plist: %w", e)
	}
	var unit struct {
		Label            string
		ProgramArguments []string
	}
	if e = json.Unmarshal(b, &unit); e != nil {
		return e
	}
	want := []string{p.Binary, "manager", "run", "--dir", p.Dir}
	if unit.Label != p.Unit || strings.Join(unit.ProgramArguments, "\x00") != strings.Join(want, "\x00") {
		return errors.New("original unit arguments differ from the explicit manager target")
	}
	b, e = c.Command(ctx, "/bin/launchctl", "print", c.service(p))
	if e != nil {
		return fmt.Errorf("original manager is not loaded: %w", e)
	}
	fields := launchFields(string(b))
	if fields["path"] != p.PlistPath || fields["program"] != p.Binary || strings.Join(launchArguments(string(b)), "\x00") != strings.Join(want, "\x00") {
		return errors.New("loaded unit path or executable differs")
	}
	if !live {
		return nil
	}
	pid, e := strconv.Atoi(fields["pid"])
	if e != nil || pid <= 0 {
		return errors.New("original manager has no running PID")
	}
	return c.checkProcess(ctx, p, pid)
}
func launchFields(s string) map[string]string {
	m := map[string]string{}
	for _, line := range strings.Split(s, "\n") {
		k, v, ok := strings.Cut(strings.TrimSpace(line), " = ")
		if ok && (k == "path" || k == "program" || k == "pid") {
			m[k] = strings.Trim(v, "\"")
		}
	}
	return m
}
func (c *Controller) checkProcess(ctx context.Context, p Plan, pid int) error {
	b, e := c.Command(ctx, "/bin/ps", "-ww", "-p", strconv.Itoa(pid), "-o", "uid=", "-o", "comm=")
	if e != nil {
		return e
	}
	parts := strings.Fields(string(b))
	if len(parts) < 2 || parts[0] != strconv.Itoa(p.UID) || strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(string(b)), parts[0])) != p.Binary {
		return errors.New("running manager UID or image differs")
	}
	return nil
}
func (c *Controller) service(p Plan) string { return fmt.Sprintf("gui/%d/%s", p.UID, p.Unit) }

func launchArguments(s string) []string {
	var args []string
	reading := false
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line == "arguments = {" {
			reading = true
			continue
		}
		if reading {
			if line == "}" {
				return args
			}
			args = append(args, strings.Trim(line, "\""))
		}
	}
	return nil
}
