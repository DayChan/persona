package nodecmd

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/nwverse/persona/server/internal/clitext"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/node"
	"github.com/nwverse/persona/server/internal/nodeupdate"
	"github.com/nwverse/persona/server/internal/osproc"
	"github.com/nwverse/persona/server/internal/sysmon"
	"github.com/nwverse/persona/server/internal/tunnel"
)

// NodeUsage is the node part of the usage, also what `personad node -h` prints.
const NodeUsage = `  personad node --stdio     run the node protocol on stdin/stdout (started by the hub over ssh); with a resident
                           node running on this machine, relay to it instead
  personad node --attach    resident node (launchd, in the login session): serves what --stdio relays, so what it
                           starts can use the login keychain (docs/server/server.md「常驻 node」); set up for tailcat,
                           also reachable by the hub over tailcat
  personad node --attach --supervised
                           the same, run by something other than systemd or launchd that starts it again whenever it
                           exits (a container's entry script, supervisord, runit, s6): the hub can then update it
                           (docs/operations/link.md "Without systemd"). Not for nohup or cron @reboot: nothing restarts those
  personad node tailcat init --hub <key> [--derp-map-url <url>]
                           let the hub reach this machine over tailcat when it cannot ssh here; prints the address
                           for hub.json (docs/server/server.md「tailcat 打洞」)
  personad node tailcat addr
                           print this machine's tailcat address again
  personad node doctor [--json] [--fix]
                           read-only check of this machine's node: node.sock / runtime.sock answer, the generated hook,
                           MCP and plugin files match this binary, the programs they run exist, claude's version, the
                           Mac LaunchAgent's ProcessType, and whether what claude reads at start can be read;
                           --fix rewrites Persona's own generated files, and on a Mac writes ProcessType=Interactive
                           into the LaunchAgent and reloads it (docs/integrations/claude-integration.md「node doctor」)
  personad node reset-uid   give this computer a new machine uid (~/.persona/machine-uid): for a virtual machine cloned
                           with another's home directory, which hubs would take for that one (docs/server/mesh.md)`

// checkNodeStdioArgs accepts what runs the node protocol on stdin/stdout: no argument (as before) or --stdio. Anything
// else is a mistyped subcommand, which would otherwise wait on stdin without a word.
func checkNodeStdioArgs(args []string) error {
	if len(args) == 0 || len(args) == 1 && args[0] == "--stdio" {
		return nil
	}
	return fmt.Errorf("不认识的 node 子命令 %q：可用的有 --stdio、--attach、tailcat、doctor、reset-uid（%s node -h 看用法）", strings.Join(args, " "), CommandName())
}

// UsageError is a usage mistake: main prints it as it is (no log timestamp) and exits 2 (clitext).
type UsageError = clitext.UsageError

// CommandName is the program's name in what it prints (clitext.CommandName).
func CommandName() string { return clitext.CommandName() }

// Named is a usage text (written with personad) with the program's name as it was run (clitext.Named).
func Named(text string) string { return clitext.Named(text) }

// IsHelpArg: -h or --help.
func IsHelpArg(a string) bool { return clitext.IsHelpArg(a) }

// noExtraArgs refuses arguments after a node subcommand that takes none: reset-uid cannot be undone, so a mistyped
// flag after it must not go ahead as if it were not there.
func noExtraArgs(cmd string, rest []string) error {
	if len(rest) == 0 {
		return nil
	}
	return fmt.Errorf("node %s 不带参数，多了 %q（%s node -h 看用法）", cmd, strings.Join(rest, " "), CommandName())
}

// exitNodeUsage is a usage mistake under `personad node`: the error, the node usage, exit 2.
func exitNodeUsage(err error) {
	fmt.Fprintln(os.Stderr, err)
	fmt.Fprintln(os.Stderr, Named(NodeUsage))
	os.Exit(2)
}

// residentArgs reads what may follow --attach: --supervised, at most once. Anything else is refused rather than
// ignored: a mistyped flag would leave the node quietly unable to take updates from the hub.
func residentArgs(args []string) (supervised bool, err error) {
	for _, a := range args {
		if a != "--supervised" || supervised {
			return false, fmt.Errorf("node --attach takes only --supervised, not %q (%s node -h for usage)", strings.Join(args, " "), CommandName())
		}
		supervised = true
	}
	return supervised, nil
}

// runResident is `personad node --attach`: relays over ssh, and the hub over tailcat when this machine is set up for
// it. A new binary arriving over tailcat ends it (exit 0): its supervisor starts the new one. supervised: started with
// --supervised, something other than systemd or launchd restarts it when it exits (nodeupdate.Options).
func runResident(ctx context.Context, b Build, supervised bool) error {
	// tailcat stops only after Attach has closed the hub's connection: torn down first, the close would never reach
	// the hub, which would then take the dead connection for a live one until its pings went unanswered.
	tctx, tstop := context.WithCancel(context.Background())
	defer tstop()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	restart := make(chan struct{})
	updater := nodeupdate.New(b.Version, func() { close(restart); cancel() }, nodeupdate.Options{Supervised: supervised})
	node.SetResident(b.Program, b.Version)
	defer updater.Close()
	if updater.RestartRequired() {
		return syscall.Exec(updater.Executable(), os.Args, os.Environ())
	}
	logger := log.New(os.Stderr, "", log.LstdFlags)
	conns, stopped, err := node.ServeTailcat(tctx, logger, func() {
		logger.Printf("tailcat: a new personad is installed; exiting so it starts")
		cancel()
	})
	if err != nil {
		logger.Printf("tailcat: %v (serving relays over ssh only)", err) // a broken key file must not stop the node
	}
	// SIGHUP (a deploy: scripts/deploy/node-launchd.sh restart-idle) restarts on the binary now installed, once no
	// conversation is answering or running background tasks: they go on across hub restarts, not across this one.
	restartIdle := make(chan struct{})
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		select {
		case <-hup:
		case <-ctx.Done():
			return
		}
		logger.Printf("restart asked for: restarting once no conversation is busy")
		for node.AssistantsBusy() {
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
		close(restartIdle)
		cancel()
	}()
	// The runtime socket is the resident node's, not one connection's: runtimes (DSH…) stay connected while the hub
	// reconnects, and connect before it does (node/runtimehost.go).
	err = node.Attach(ctx, func() (*node.Node, error) {
		n, err := node.New(b.Version)
		if err == nil {
			n.SetUpdater(updater)
			n.Program = b.Program
		}
		return n, err
	}, conns, node.NewRuntimeHost(b.Version, ""))
	tstop()
	<-stopped
	select {
	case <-restart:
		return syscall.Exec(updater.Executable(), os.Args, os.Environ())
	case <-restartIdle:
		logger.Printf("restarting on %s", updater.Executable())
		return syscall.Exec(updater.Executable(), os.Args, os.Environ())
	default:
		return err
	}
}

// runNodeTailcat is `personad node tailcat init|addr`.
func runNodeTailcat(ctx context.Context, args []string) error {
	if len(args) == 0 {
		exitNodeUsage(fmt.Errorf("node tailcat 要跟 init 或 addr（%s node -h 看用法）", CommandName()))
	}
	switch args[0] {
	case "addr":
		if err := noExtraArgs("tailcat addr", args[1:]); err != nil {
			exitNodeUsage(err)
		}
		addr, err := node.TailcatAddr()
		if err != nil {
			return err
		}
		fmt.Println(addr)
		return nil
	case "init":
		fs := flag.NewFlagSet("tailcat init", flag.ContinueOnError)
		hubKey := fs.String("hub", "", "the hub's `key`: what "+CommandName()+" hub tailcat-key prints on the hub's machine")
		derpMap := fs.String("derp-map-url", "", "a DERP map of your own relays instead of Tailscale's public ones")
		if err := fs.Parse(args[1:]); err != nil {
			return err
		}
		if fs.NArg() > 0 {
			exitNodeUsage(fmt.Errorf("node tailcat init 只认 --hub 和 --derp-map-url，多了 %q（%s node -h 看用法）", strings.Join(fs.Args(), " "), CommandName()))
		}
		if *hubKey == "" {
			return fmt.Errorf("--hub is required: run `%s hub tailcat-key` on the hub's machine", CommandName())
		}
		ictx, cancel := context.WithTimeout(ctx, time.Minute)
		defer cancel()
		k, err := tunnel.InitNodeKey(ictx, tunnel.NodeKeyPath(), *hubKey, *derpMap)
		if err != nil {
			return err
		}
		fmt.Fprintf(os.Stderr, "set up %s (DERP region %d). In the hub's hub.json, this machine's entry gets\n"+
			"  \"tailcat_addr\": the line below (a secret: it lets the hub's key in)\n"+
			"then restart the resident node here (scripts/deploy/node-launchd.sh install) and the hub.\n", tunnel.NodeKeyPath(), k.RegionID)
		fmt.Println(k.Addr())
		return nil
	}
	exitNodeUsage(fmt.Errorf("不认识的 node tailcat 子命令 %q：可用的有 init、addr（%s node -h 看用法）", args[0], CommandName()))
	return nil
}

// sysmonMaxCount caps `sysmon [n]` at a day of 10-second samples: a bigger n never ends in practice, and the
// largest ints overflowed n+1 in the summary.
const sysmonMaxCount = 8640

// sysmonCount reads `sysmon [n]`: how many samples after the first, 3 when not given.
func sysmonCount(args []string) (int, error) {
	if len(args) == 0 {
		return 3, nil
	}
	n, err := strconv.Atoi(args[0])
	if len(args) > 1 || err != nil || n < 0 {
		return 0, fmt.Errorf("usage: %s sysmon [n]   (n: how many 10-second samples after the first, 0 or more)", CommandName())
	}
	if n > sysmonMaxCount {
		return 0, fmt.Errorf("%s sysmon: n is at most %d (a day of 10-second samples), not %d", CommandName(), sysmonMaxCount, n)
	}
	return n, nil
}

// sysmonSamples calls sample 1 + n times, one `every` apart, and says how many it took: fewer when ctx ends
// (Ctrl-C), which also cuts the wait in between short.
func sysmonSamples(ctx context.Context, n int, every time.Duration, sample func()) int {
	taken := 0
	for i := 0; i <= n && ctx.Err() == nil; i++ {
		if i > 0 {
			t := time.NewTimer(every)
			select {
			case <-ctx.Done():
				t.Stop()
				return taken
			case <-t.C:
			}
		}
		sample()
		taken++
	}
	return taken
}

// runSysmon shows what a node would report (docs/server/machines.md), and the CPU time it took: how the collection cost
// in the docs was measured.
func runSysmon(ctx context.Context, n int) {
	enc := json.NewEncoder(os.Stdout)
	start := time.Now()
	_ = enc.Encode(sysmon.ReadSystem(ctx))
	fmt.Printf("system info: %v\n", time.Since(start).Round(time.Millisecond))
	c := sysmon.NewCollector(ctx)
	defer c.Close()
	cpu0 := osproc.CPUTime()
	began := time.Now()
	var took time.Duration
	taken := sysmonSamples(ctx, n, 10*time.Second, func() {
		t := time.Now()
		s := c.Sample(t)
		took += time.Since(t)
		_ = enc.Encode(s)
	})
	if taken == 0 {
		fmt.Println("0 samples: stopped before the first")
		return
	}
	cpu := osproc.CPUTime() - cpu0
	wall := time.Since(began)
	fmt.Printf("%d samples: %v each; %s CPU %v over %v (%.3f%%), not counting iostat / vm_stat on macOS\n",
		taken, (took / time.Duration(taken)).Round(time.Microsecond), CommandName(), cpu.Round(time.Millisecond), wall.Round(time.Second),
		100*cpu.Seconds()/wall.Seconds())
}

// resetMachineUID makes a new ~/.persona/machine-uid; the node picks it up when it starts again.
func resetMachineUID() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	uid, err := node.ResetMachineUID(identity.DataDir(home))
	if err != nil {
		return err
	}
	fmt.Println(uid)
	fmt.Fprintln(os.Stderr, "已换成新的机器 uid。重启这台电脑上的常驻 node（scripts/deploy/node-launchd.sh install 会重启它），hub 下次连上时就用新的。")
	return nil
}
