package hub

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/hubedge"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/update"
)

// The hub's edge (internal/hubedge, docs/operations/hub-restart.md "Blue-green switch"): `personad hub run` holds the ports and runs the
// hub as a slot behind them (`personad hub slot`), so an update or a deploy starts the new binary next to the one
// serving and switches only once it is up. Without the edge (Windows, Docker, PERSONA_HUB_EDGE=0) the hub serves by
// itself, as before.

// EnvEdge set to 0 runs the hub without its edge.
const EnvEdge = "PERSONA_HUB_EDGE"

// maxSocketPath keeps the slots' socket paths under every system's limit for a Unix socket address (macOS: 104).
const maxSocketPath = 100

// ControlPath is the edge's control socket (`personad hub switch`).
func ControlPath() string { return filepath.Join(edgeRunDir(), "hub-edge.sock") }

func edgeRunDir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.DataDir(home), "run")
}

// edgeOff says why this hub serves without its edge, or "" when it runs behind one.
func edgeOff(b update.Build) string {
	switch {
	case runtime.GOOS != "linux" && runtime.GOOS != "darwin":
		return "the edge runs on Linux and macOS"
	case b.Channel == update.Docker:
		return "a Docker hub is updated by its image"
	case identity.Getenv(EnvEdge) == "0":
		return EnvEdge + "=0"
	}
	if p := filepath.Join(edgeRunDir(), "slot-a-65535.sock"); len(p) > maxSocketPath {
		return "the data directory's path is too long for its sockets (" + p + ")"
	}
	return ""
}

// Serve is `personad hub run`: the hub behind its edge, or by itself where the edge does not run.
func Serve(ctx context.Context, b update.Build) error {
	claimInherited() // before anything can start a process (listen.go)
	logger := log.New(os.Stderr, "", log.LstdFlags)
	if why := edgeOff(b); why != "" {
		if identity.Getenv(EnvEdge) != "" || runtime.GOOS == "linux" || runtime.GOOS == "darwin" {
			logger.Printf("hub: serving without the edge: %s", why)
		}
		return Run(ctx, b)
	}
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	binary, err := runningBinary()
	if err != nil {
		return err
	}
	addrs := []string{cfg.Listen}
	if cfg.Public.TLSListen != "" {
		addrs = append(addrs, cfg.Public.TLSListen)
	}
	var lns []net.Listener
	for i, a := range addrs {
		ln, activated, err := listenTCP(a)
		if err != nil && i > 0 && tlsAddressGone(a, err) {
			// The TLS address is not this machine's now (DHCP moved it): serve without it, as the hub does by itself
			// (public.go startPublic); the slot finds no socket for it and turns TLS off.
			logger.Printf("hub edge: %s is not an address of this machine now: no TLS port", a)
			continue
		}
		if err != nil {
			for _, l := range lns {
				_ = l.Close()
			}
			return err
		}
		if activated {
			logger.Printf("hub edge: %s is the socket systemd passed", ln.Addr())
		}
		lns = append(lns, ln)
	}
	logUnclaimedInherited(logger.Printf)
	return hubedge.Run(ctx, hubedge.Options{
		Listeners: lns, Binary: binary, Args: []string{"hub", "slot"},
		RunDir: edgeRunDir(), Control: ControlPath(), Version: b.Version, Logf: logger.Printf,
		RestoreUpdate: restoreUpdate,
	})
}

// restoreUpdate is the edge's way back from an update whose new slot failed (hubedge.Options.RestoreUpdate): the
// pending marker's binaries go back, the rollback is written down for the old version to record (history, bad build:
// selfupdate.go recordRolledBack), and the binary at the hub's path runs again.
func restoreUpdate(build int64, cause error) (string, bool) {
	return restoreUpdateIn(update.Dir(), build, cause)
}

func restoreUpdateIn(dir string, build int64, cause error) (string, bool) {
	var p update.Pending
	if ok, err := update.Load(update.PendingPath(dir), &p); err != nil || !ok || (build != 0 && p.ToBuild != build) {
		return "", false
	}
	if err := update.RestoreAll(p); err != nil {
		log.Printf("hub edge: restore %s: %v", p.Prev, err)
	}
	l := i18n.Default
	reason := i18n.T(l, "hub-selfupdate.reason_not_confirmed")
	if cause != nil {
		reason = i18n.T(l, "hub-selfupdate.reason_no_takeover", i18n.Args{"error": cause.Error()})
	}
	if err := update.Save(update.RolledBackPath(dir), update.RolledBack{FromVersion: p.FromVersion, ToVersion: p.ToVersion,
		ToBuild: p.ToBuild, Started: p.Started, At: time.Now(), Reason: reason, Auto: p.Auto}); err != nil {
		log.Printf("hub edge: save rollback marker: %v", err)
	}
	_ = os.Remove(update.PendingPath(dir))
	return p.Target, true
}

// RunSlot is `personad hub slot`: the hub as the edge started it. A standby checks itself (its config, the database)
// and waits; once activated, or started active, it serves on the edge's sockets until the edge releases it.
func RunSlot(ctx context.Context, b update.Build) error {
	claimInherited() // before anything can start a process (listen.go); the edge's pipes stay readable here
	slot, err := hubedge.FromEnv()
	if err != nil {
		return err
	}
	if slot == nil {
		return errors.New("hub slot: only the hub's edge starts slots (personad hub run)")
	}
	slot.Hello(b.Version, b.Number)
	logger := slotLogger(slot)
	// An update that keeps failing goes back before anything else can fail, the config included (selfupdate.go).
	updating := newUpdater(&Hub{build: b, version: b.Version, logger: logger, slot: slot}).boot()
	fail := func(err error) error {
		slot.Failed(err.Error())
		return err
	}
	cfg, err := LoadConfig()
	if err != nil {
		return fail(err)
	}
	if slot.Standby {
		cctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		err := db.Check(cctx, cfg.DatabaseURL)
		cancel()
		if err != nil {
			return fail(fmt.Errorf("database: %w", err))
		}
		if err := slot.StandbyReady(ctx); err != nil {
			if errors.Is(err, hubedge.ErrReleased) {
				return nil // not wanted after all
			}
			return err
		}
	}
	// The edge's release is the planned stop, like SIGTERM (restart.go beginStop).
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-slot.Released():
			cancel()
		case <-ctx.Done():
		}
	}()
	if err := runConfigured(ctx, b, cfg, updating, slot); err != nil {
		return fail(err)
	}
	return nil
}

// slotLogger prefixes a slot's lines with its name: two slots write to the same journal while they hand over.
func slotLogger(slot *hubedge.Slot) *log.Logger {
	prefix := ""
	if slot != nil {
		prefix = "[slot " + slot.Name + "] "
	}
	return log.New(os.Stderr, prefix, log.LstdFlags)
}
