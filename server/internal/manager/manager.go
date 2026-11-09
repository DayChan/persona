// Package manager is `personad manager …`: a manager made in the app (docs/assistant/managers.md, "A second manager") joins the hub
// with its one-time install code, runs, and is installed as a service of its own (systemd user unit, launchd agent,
// or a container). The manager itself is the assistant's program (assistant.RunManager) with a directory of its own.
package manager

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/managerupdate"
	"github.com/nwverse/persona/server/internal/terminalmaintenance"
	"github.com/nwverse/persona/server/internal/update"
)

// Usage is the help of `personad manager`.
const Usage = `usage:
  personad manager maintenance prepare|observe|recovery-recipe --help
                       preserve exact-session recovery material; no exit, restore or update execution
  personad manager update check|start|status|run|confirm|rollback --plan PATH
                       inspect or activate one explicit independent manager release plan
  personad manager update inventory --plan PATH --main ID --machine ID --machine-uid UID
                       read persisted main identity and two complete local node activity snapshots; writes nothing
  personad manager join <hub 地址> <安装码> [--dir 目录] [--database-url URL] [--fingerprint 指纹] [--install]
                       用 App 里「新建管理者」给的安装码换这个管理者的 token，写进目录里的 manager.json
                       （默认 ~/.persona/managers/<id>）；数据库是它自己的 Postgres：install 时在 Docker 里建
                       （容器 persona-mgr-<id>-pg），或者 --database-url 给一个
  personad manager join --builtin <hub address> [<code>] [--fingerprint <fingerprint>] [--install]
                       bind the built-in manager's own service to the hub with a code from personad hub manager-code
                       (given, or in PERSONA_MANAGER_CODE, which other users of the machine cannot read; the binding
                       goes into ~/.persona/managers/assistant/manager.json); --install: then install its service
  personad manager run [--dir 目录] [--join <hub 地址> <安装码>]
                       运行这个管理者（--join：目录里还没有 manager.json 时先换码，容器用）
  personad manager install [--dir 目录]     建好它自己的 Postgres（Docker），装成常驻服务（Linux：systemd 用户服务；Mac：launchd）
  personad manager uninstall [--dir 目录] [--purge]
                       停掉并去掉常驻服务；它的数据库容器停掉、数据卷留着（--purge：连容器和数据卷一起删掉）
  personad manager capabilities [--json]   只读列出这个程序支持的管理者能力
  personad manager status [--dir 目录]      这台电脑上的管理者和它们的服务
  personad manager install --builtin [--join <hub address>] | --check
                       install the built-in manager's own service (Linux: systemd user unit persona-manager-assistant;
                       Mac: launchd); --join: bind it first with the code in PERSONA_MANAGER_CODE, unless its binding
                       works (personad hub setup does this); --check: only say whether it can be installed here
  personad manager uninstall --builtin    stop and remove that service; its binding and data stay
  personad manager status --builtin       the built-in manager's binding and service
`

// errUsage: the arguments are wrong; the caller prints Usage.
var errUsage = errors.New("arguments")

// ErrUsage is errUsage for the command line (personad prints Usage and exits 2).
var ErrUsage = errUsage

// Main runs `personad manager <args>`.
func Main(ctx context.Context, build update.Build, args []string, stdout io.Writer) error {
	if len(args) == 0 {
		return errUsage
	}
	e := newEnv(stdout)
	switch args[0] {
	case "maintenance":
		return terminalmaintenance.Main(ctx, args[1:], stdout)
	case "update":
		return managerupdate.Main(ctx, build, args[1:], stdout)
	case "capabilities":
		return cmdCapabilities(stdout, args[1:])
	case "join":
		return cmdJoin(ctx, e, args[1:])
	case "run":
		return cmdRun(ctx, e, build, args[1:])
	case "install":
		if isBuiltin(args[1:]) {
			return cmdInstallBuiltin(ctx, e, args[1:])
		}
		return cmdInstall(ctx, e, args[1:])
	case "uninstall":
		if isBuiltin(args[1:]) {
			return cmdUninstallBuiltin(ctx, e, args[1:])
		}
		return cmdUninstall(ctx, e, args[1:])
	case "status":
		return cmdStatus(ctx, e, args[1:])
	case "-h", "--help", "help":
		fmt.Fprint(stdout, clitext.Named(Usage)) // as the program was run (persona-manager, personad)
		return nil
	}
	return errUsage
}

// --- helpers ------------------------------------------------------------------------------------------------------

// parseInterleaved parses flags that may come before, between or after the positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			if errors.Is(err, flag.ErrHelp) {
				return nil, errHelp
			}
			return nil, errUsage
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

// ConfigError is a mistake in the manager's configuration (no manager.json, no database to use): personad exits 2,
// which its systemd service does not restart on (RestartPreventExitStatus=2; a container's restart policy does, so
// manager run --join waits instead).
type ConfigError struct{ Err error }

func (e ConfigError) Error() string { return e.Err.Error() }
func (e ConfigError) Unwrap() error { return e.Err }

// errHelp: -h was asked for; the flags' help is printed already.
var errHelp = errors.New("help")

// ErrHelp is errHelp for the command line (exit 0).
var ErrHelp = errHelp

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	if len(s) > 200 {
		s = s[:200]
	}
	return s
}

// shellQuote makes s one word for sh.
func shellQuote(s string) string {
	if s != "" && strings.Trim(s, "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789/._-+=:@") == "" {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// dataDir is this machine's data directory (~/.persona), for the binary the service runs.
func dataDir() string { return identity.HomeDir() }
