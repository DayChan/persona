package manager

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync/atomic"

	"github.com/nwverse/persona/server/internal/assistant"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/managerconf"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/update"
)

// --- run ----------------------------------------------------------------------------------------------------------

// restart starts the program again once a new token is there (reexec; tests: nil, run again in this process).
var restart = reexec

func cmdRun(ctx context.Context, e *env, build update.Build, args []string) error {
	fs := flag.NewFlagSet("manager run", flag.ContinueOnError)
	fs.SetOutput(e.stdout)
	dir := fs.String("dir", "", "管理者的目录（这台只有一个管理者时可以不写）")
	join := fs.Bool("join", false, "目录里还没有 manager.json 时，先用后面的 <hub 地址> <安装码> 换码")
	dbURL := fs.String("database-url", "", "和 --join 一起：它自己的 Postgres 库（容器里经环境变量 "+databaseEnv+" 给，不放命令行）")
	fp := fs.String("fingerprint", "", "和 --join 一起：hub 的 TLS 指纹")
	plain := fs.Bool("allow-plaintext", false, "和 --join 一起：允许公网上的明文 http hub 地址")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return err
	}
	if *join != (len(pos) == 2) || (!*join && len(pos) != 0) {
		return errUsage
	}
	d := *dir
	if *join && (d == "" || !fileExists(filepath.Join(d, assistant.ManagerFile))) {
		dbAt := *dbURL
		if dbAt == "" {
			dbAt = e.getenv(databaseEnv)
		}
		joined, _, err := Join(ctx, JoinOptions{Hub: pos[0], Code: pos[1], Dir: d, DatabaseURL: dbAt, Fingerprint: *fp,
			CheckDocker: e.dockerProblem, Warn: e.stdout, AllowPlaintext: *plain})
		var ce ConfigError
		switch {
		case err == nil:
			d = joined
			fmt.Fprintf(e.stdout, "加入了 hub，目录 %s\n", d)
		case errors.As(err, &ce) && d == "":
			// No directory to wait on: watching for "the one manager of this computer" would take up another
			// manager's directory when there is exactly one. A container always names its --dir; anyone else gets the
			// configuration error (exit 2).
			return err
		case errors.As(err, &ce):
			// The same command can't do better next time (a used or expired code, a refused plaintext join, no
			// database it can have), and a container's --restart unless-stopped starts it again on any exit, exit 2 too: wait,
			// not dialing, until a join run by hand into the directory writes manager.json, as after a revoke.
			fmt.Fprintf(e.stdout, "manager: 没加入 hub，同样的命令再试也不会成：%v。在等：在 App 里拿一个新安装码，"+
				"再运行一次新的安装命令（没加入过的容器会用新码重建），或者运行 personad manager join <hub 地址> <安装码> --dir %s\n", err, shellQuote(d))
			if err = waitJoined(ctx, d); err != nil {
				return err
			}
			fmt.Fprintf(e.stdout, "manager: 有了 manager.json，目录 %s\n", d)
		default:
			return err
		}
	}
	if d == "" {
		if d, err = onlyManager(); err != nil {
			return err
		}
	}
	return e.runManager(ctx, build, d)
}

// Run runs the manager in dir until ctx ends. When the hub refuses its token it says so and waits — not dialing —
// until manager.json has another one (`personad manager join` with a new code, into the same directory), so a
// service manager that restarts it does not turn into a loop of refused connections.
func Run(ctx context.Context, build update.Build, dir string, out io.Writer) error {
	return newEnv(out).runManager(ctx, build, dir)
}

func (e *env) runManager(ctx context.Context, build update.Build, dir string) error {
	osproc.SealInherited() // what a parent left inheritable reaches none of what the manager starts (osproc)
	out := e.stdout
	env := os.Environ() // as it started: RunManager points PERSONA_HOME at dir
	c, err := assistant.ReadManagerConfig(dir)
	if err != nil {
		return ConfigError{err} // restarting cannot fix it (the service's unit does not, exit 2)
	}
	// Its own Postgres comes up with it (started again after a reboot; made here when install did not).
	if _, err := e.ownDatabase(ctx, dir, c); err != nil {
		return err
	}
	for {
		before, _ := assistant.ReadManagerConfig(dir)
		err := assistant.RunManager(ctx, build, dir)
		if !errors.Is(err, bus.ErrRevoked) {
			return err
		}
		id := ""
		if before != nil {
			id = before.ID
		}
		fmt.Fprintf(out, "manager: [%s] hub 不再认这个管理者的 token（在 App 里删掉了，或者在别处重新装了）：先不连 hub，每 %s 再试一次。"+
			"要接着用这个目录：在 App 里给它拿一个新安装码，运行 personad manager join <hub 地址> <安装码> --dir %s\n", id, revokedRetry, shellQuote(dir))
		if err := awaitRestart(ctx, dir, before, out); err != nil {
			return err
		}
		if restart != nil {
			if err := restart(env); err != nil {
				fmt.Fprintf(out, "manager: [%s] could not start again in place (%v): running it again here\n", id, err)
			}
		}
	}
}

// RunBuiltin is the built-in manager as a service of its own (`persona-manager assistant` with
// PERSONA_ASSISTANT_DEPLOY=service, docs/assistant/managers.md "The built-in manager as its own service"), on the
// binding in its manager.json. Not bound is a configuration error (exit 2: its unit does not restart it). Once the hub
// refuses its token (bound again, or a hub that starts the built-in manager itself again: assistant.mode child, a
// version from before the split) nothing of it runs until manager.json has another token or the hub takes this one
// again (awaitRestart), so it never runs next to the one that has the token; then it starts again in place. It also
// starts again in place on a new file where it runs from, one that runs, once it is idle (binaryWatch): nothing else
// restarts its service after an update.
func RunBuiltin(ctx context.Context, build update.Build, out io.Writer) error {
	env := os.Environ()
	home := dataDir()
	// One at a time: a second one on the same binding would take turns on the bus with the first, both running the
	// computers and the extensions.
	if err := os.MkdirAll(managerconf.DefaultDir(home), 0o700); err != nil {
		return err
	}
	if err := lockFile(filepath.Join(managerconf.DefaultDir(home), builtinLock)); err != nil {
		if errors.Is(err, errLocked) {
			return ConfigError{errors.New("another persona-manager assistant runs on this data directory as a service already")}
		}
		return err
	}
	watch := newBinaryWatch(out)
	for {
		before, err := managerconf.ReadBinding(home)
		if err != nil { // not bound, or a manager.json that does not read: restarting cannot fix it (exit 2)
			return ConfigError{err}
		}
		// A new binary (an update): once it runs and nothing is going on, it stops and starts again on that one, as
		// the hub's child would with the hub (watchBinary).
		run, stop := context.WithCancel(ctx)
		var idle atomic.Pointer[assistant.Idle]
		var renew atomic.Bool
		watched := make(chan struct{})
		if watch != nil {
			go func() {
				defer close(watched)
				if watch.next(run, func() assistant.Idle {
					if p := idle.Load(); p != nil {
						return *p
					}
					return nil
				}) {
					renew.Store(true)
					stop()
				}
			}()
		} else {
			close(watched)
		}
		err = runAssistant(run, build, nil, assistant.MainHooks{Ready: func(p assistant.Idle) { idle.Store(&p) }})
		stop()
		<-watched
		if renew.Load() && ctx.Err() == nil {
			watch.restartOnNew(ctx, env) // returns when it did not: the manager runs again on this code
			continue
		}
		if errors.Is(err, managerconf.ErrNotBound) {
			return ConfigError{err}
		}
		if !errors.Is(err, bus.ErrRevoked) {
			return err
		}
		fmt.Fprintf(out, "assistant: the hub no longer takes the built-in manager's token (bound again, or the hub "+
			"starts it itself again): not connecting, trying the same token every %s. To bind it again: persona-hub "+
			"manager-code on the hub's machine, then persona-manager join --builtin <hub address> <code>\n", revokedRetry)
		c := &assistant.ManagerConfig{ID: assistantwire.AssistantID, Hub: before.Hub, Token: before.Token, Fingerprint: before.Fingerprint}
		if err := awaitRestartWith(ctx, c, builtinToken(home), out); err != nil {
			return err
		}
		if restart != nil {
			// The file there may be a new one by now: only one that runs (as for a new binary, binaryWatch).
			if watch != nil {
				if err := checkBinary(ctx, watch.path); err != nil {
					if ctx.Err() != nil {
						return ctx.Err()
					}
					fmt.Fprintf(out, "assistant: %v: running it again here, on the code it has\n", err)
					if fi, err := os.Stat(watch.path); err == nil {
						watch.seen = fi
					}
					continue
				}
			}
			if err := restart(env); err != nil {
				fmt.Fprintf(out, "assistant: could not start again in place (%v): running it again here\n", err)
			}
		}
	}
}

// runAssistant is the built-in manager itself (tests: a stand-in).
var runAssistant = assistant.MainWith

// builtinLock is the file in the built-in manager's directory its own service holds a lock on while it runs.
const builtinLock = ".service.lock"

// errLocked: another process holds the lock (lockFile).
var errLocked = errors.New("locked by another process")

// held keeps the locked files open for the life of the process.
var held []*os.File

// builtinToken reads the token in the built-in manager's binding ("" when it does not read).
func builtinToken(home string) func() string {
	return func() string {
		b, err := managerconf.ReadBinding(home)
		if err != nil {
			return ""
		}
		return b.Token
	}
}
