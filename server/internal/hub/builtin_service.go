package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerconf"
)

// The built-in manager as a service of its own (docs/assistant/managers.md "The built-in manager as its own
// service"): hub.json's assistant.mode says the hub no longer starts it (ManagerMode), and a one-time code
// (ManagerCode) binds the service to this hub: `persona-manager join --builtin` trades it for the built-in manager's
// token (POST /api/v1/bus/join, builtinJoinRefused) and keeps it in its own manager.json. The hub never writes that
// file, and in service mode never makes the built-in manager a new token itself.

// ManagerMode is `persona-hub manager-mode [child|service]`: hub.json's assistant.mode, shown, or set (the other keys
// as they were). A running hub reads it when it starts: the change takes effect with `persona-hub switch` (or a
// restart of its service). Help asked for prints the usage on out (exit 0); a usage mistake is a clitext.UsageError
// (exit 2), found before the state directory is made.
func ManagerMode(args []string, out io.Writer) error {
	if slices.ContainsFunc(args, isManagerModeHelp) {
		_, err := fmt.Fprintln(out, clitext.Named(managerModeUsage))
		return err
	}
	if err := checkManagerModeArgs(args); err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	dir, err := identity.PrepareHome(home)
	if err != nil {
		return err
	}
	return managerMode(dir, args, out)
}

// managerModeUsage is `hub manager-mode`'s usage (clitext.Named puts the program's name in).
const managerModeUsage = `usage: personad hub manager-mode [child|service]
  (nothing)  show hub.json's assistant.mode
  child      the hub starts the built-in manager (the default)
  service    the built-in manager is a service of its own
  A change is taken at the hub's next start: personad hub switch (or restart its service).`

func isManagerModeHelp(a string) bool { return a == "help" || clitext.IsHelpArg(a) }

// checkManagerModeArgs refuses anything but child, service or nothing.
func checkManagerModeArgs(args []string) error {
	switch {
	case len(args) > 1:
		return clitext.UsageError("manager-mode takes child, service or nothing\n" + clitext.Named(managerModeUsage))
	case len(args) == 1 && args[0] != AssistantModeChild && args[0] != AssistantModeService:
		return clitext.UsageError(fmt.Sprintf("manager-mode %q: child or service\n%s", args[0], clitext.Named(managerModeUsage)))
	}
	return nil
}

func managerMode(dir string, args []string, out io.Writer) error {
	if err := checkManagerModeArgs(args); err != nil {
		return err
	}
	if len(args) == 0 {
		cfg, err := readHubJSON(dir)
		if err != nil {
			return err
		}
		mode := AssistantModeChild
		if a, err := cfg.Object("assistant"); err == nil && a != nil {
			if raw, ok := a.Get("mode"); ok {
				var m string
				if json.Unmarshal(raw, &m) == nil && m != "" {
					mode = m
				}
			}
			var external bool
			if raw, ok := a.Get("external"); ok && json.Unmarshal(raw, &external) == nil && external {
				mode += " (assistant.external: it runs elsewhere)"
			}
		}
		_, err = fmt.Fprintln(out, mode)
		return err
	}
	mode := args[0]
	err := editHubJSON(dir, func(o *jsonObject) error {
		a, err := o.Object("assistant")
		if err != nil {
			return err
		}
		if a == nil {
			a = &jsonObject{}
		}
		if mode == AssistantModeChild {
			a.Del("mode") // the default
		} else if err := a.Set("mode", mode); err != nil {
			return err
		}
		o.SetObject("assistant", a)
		return nil
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "assistant.mode is %s in %s/hub.json. The running hub takes it at its next start: "+
		"persona-hub switch (or restart its service).\n", mode, dir)
	return err
}

// ManagerCode is `persona-hub manager-code`: a one-time code (10 minutes) that binds the built-in manager's own
// service to this hub. Only in service mode: while the hub starts the built-in manager itself, a second one would run
// next to it (the hub refuses to trade such a code then, too).
func ManagerCode(ctx context.Context, out io.Writer) error {
	cfg, err := runningHubConfig()
	if err != nil {
		return err
	}
	if !cfg.Assistant.ServiceBound() {
		return errors.New("the built-in manager is not a service of its own here (assistant.mode service, not " +
			"assistant.external): first persona-hub manager-mode service, then persona-hub switch (or restart its service)")
	}
	code, until, err := builtinCode(ctx, cfg)
	if err != nil {
		return err
	}
	return writeManagerCode(out, code, until, cfg.Listen)
}

// writeManagerCode prints the code and how to bind with it: in the environment, where other users of the machine
// cannot read it (a command line they can, while it runs).
func writeManagerCode(out io.Writer, code string, until time.Time, listen string) error {
	_, err := fmt.Fprintf(out, "%s\n\nGood until %s, once. On this machine (where the built-in manager's service runs), with "+
		"the code in the environment, where other users of the machine cannot read it:\n"+
		"  PERSONA_MANAGER_CODE=<the code> persona-manager join --builtin %s --install\n", code, until.UTC().Format("2006-01-02 15:04 MST"), managerconf.LocalHubURL(listen))
	return err
}

// MintBuiltinCode is ManagerCode's code alone, for a program that binds the built-in manager's own service itself (a
// container's supervisor, internal/supervise): refused unless the built-in manager is a bound service here.
func MintBuiltinCode(ctx context.Context) (string, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return "", err
	}
	if !cfg.Assistant.ServiceBound() {
		return "", errors.New("the built-in manager is not a service of its own here (assistant.mode service, not assistant.external)")
	}
	code, _, err := builtinCode(ctx, cfg)
	return code, err
}

// VoidBuiltinCode voids a code MintBuiltinCode made when it was not traded (it is still good): nothing when it was.
func VoidBuiltinCode(ctx context.Context, code string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	return voidBuiltinCode(ctx, cfg, code)
}
