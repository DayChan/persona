package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
	"github.com/nwverse/persona/server/internal/update"
)

// begin starts an update the user asked for; now skips waiting for idle ("Update now" in the second confirmation).
func (u *updater) begin(ctx context.Context, now bool) error {
	if !u.h.build.SelfUpdates() {
		return u.whyNot()
	}
	u.mu.Lock()
	if u.running {
		if now {
			u.now = true
		}
		u.mu.Unlock()
		return nil
	}
	r := u.release
	u.mu.Unlock()
	if r == nil || !u.h.build.Newer(r.Manifest.Build) {
		return errNoUpdate
	}
	if err := u.unreplaced(r.Manifest); err != nil {
		return err
	}
	u.mu.Lock()
	u.now = now
	u.mu.Unlock()
	go u.install(ctx, false)
	return nil
}

// install is one update: download, verify, wait for idle, swap, restart. It returns only when it did not restart.
func (u *updater) install(ctx context.Context, auto bool) {
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return
	}
	u.running = true
	r := u.release
	u.mu.Unlock()
	started := time.Now()
	// What this update downloads goes under staged. Once it fails nothing reads that again (another try downloads
	// afresh), so it goes before another install can start.
	staged := u.stagedDir(r.Manifest.Build)
	fail := func(err error) {
		u.removeDownloads(staged)
		u.mu.Lock()
		u.running, u.now = false, false
		u.busy = nil
		u.mu.Unlock()
		why := i18n.Localize(err, u.h.userLang())
		u.set("failed", "", why)
		u.record(ctx, updateRecord{FromVersion: u.h.version, ToVersion: r.Manifest.Version, ToBuild: r.Manifest.Build,
			Started: started, Ended: time.Now(), Result: "failed", Reason: why, Auto: auto})
		u.h.logf("update to %s: %v", r.Manifest.Version, err)
	}
	m := r.Manifest
	file, ok := m.Files[runtime.GOOS+"-"+runtime.GOARCH]
	if !ok {
		fail(i18n.Err("hub-selfupdate.err_no_binary", i18n.Args{"platform": runtime.GOOS + "/" + runtime.GOARCH}))
		return
	}
	if err := u.unreplaced(m); err != nil {
		fail(err)
		return
	}
	u.set("downloading", "hub", "")
	bin, err := update.Download(ctx, u.client, u.h.build.UpdateURL, file, staged)
	if err != nil {
		fail(err)
		return
	}
	nodes, err := u.stageNodes(ctx, m, staged, bin)
	if err != nil {
		fail(err)
		return
	}
	if err := update.CheckBinary(ctx, bin, m); err != nil {
		fail(err)
		return
	}
	// The programs the hub runs beside it (personad-wechat): this platform's build of each, checked like personad. One
	// the release has no build of keeps its old file (persona-hub and persona-manager only when no service runs it:
	// unreplaced).
	programs := map[string]string{} // name -> the downloaded binary
	for _, name := range update.ProgramNames {
		f, ok := m.Programs[name][runtime.GOOS+"-"+runtime.GOARCH]
		if !ok {
			continue
		}
		u.set("downloading", name, "")
		pb, err := update.Download(ctx, u.client, u.h.build.UpdateURL, f, filepath.Join(staged, "programs"))
		if err != nil {
			fail(err)
			return
		}
		if err := update.CheckBinary(ctx, pb, m); err != nil {
			fail(fmt.Errorf("%s: %w", name, err))
			return
		}
		programs[name] = pb
	}
	// Wait until nothing the restart would interrupt is going on (or the user said "Update now").
	waitStarted := time.Now()
	for {
		busy := u.h.updateBusy()
		u.mu.Lock()
		u.busy = busy
		now, autoOn := u.now, u.settings.auto()
		u.mu.Unlock()
		if len(busy) == 0 || now {
			break
		}
		if auto && !autoOn {
			fail(errUpdateAgain)
			return
		}
		if !auto && time.Since(waitStarted) > u.manualIdle {
			fail(errUpdateBusy)
			return
		}
		u.set("waiting_idle", "", "")
		select {
		case <-ctx.Done():
			return
		case <-time.After(u.idleEvery):
		}
	}
	target, err := u.target()
	if err != nil {
		fail(err)
		return
	}
	if err := u.unreplaced(m); err != nil { // a service pointed at a program meanwhile
		fail(err)
		return
	}
	p := update.Pending{FromVersion: u.h.version, FromBuild: u.h.build.Number, ToVersion: m.Version, ToBuild: m.Build,
		Target: target, Started: started, Online: u.onlineIDs(), Auto: auto, PID: os.Getpid(), Args: os.Args,
		HealthURL: u.healthURL(), Nodes: nodes}
	prev, err := update.Swap(target, bin)
	if err != nil {
		fail(err)
		return
	}
	p.Prev = prev
	for _, name := range update.ProgramNames {
		if pb, ok := programs[name]; ok {
			sw, err := update.SwapProgram(filepath.Join(filepath.Dir(target), name), pb)
			p.Programs = append(p.Programs, sw)
			if err != nil {
				_ = update.RestoreAll(p)
				fail(fmt.Errorf("%s: %w", name, err))
				return
			}
		}
	}
	p.Run = runAs(u.self, p.Programs)
	if err := update.Save(update.PendingPath(u.dir), p); err != nil {
		_ = update.RestoreAll(p)
		fail(err)
		return
	}
	if u.handover != nil {
		// Blue-green (docs/operations/hub-restart.md "Blue-green switch"): the edge starts the new binary next to this one and hands over
		// only once it checked itself; this process goes on serving until then, and stops when the edge releases it.
		// The edge is the watchdog too (it goes back when the new one does not confirm the update in time).
		u.set("restarting", "", "")
		u.h.logf("update: handing over to %s", m.Version)
		if err := u.handover(ctx, p.RunPath(), m.Build); err != nil {
			_ = update.RestoreAll(p)
			_ = os.Remove(update.PendingPath(u.dir))
			fail(i18n.Err("hub-selfupdate.err_no_start", i18n.Args{"error": err.Error()}))
		}
		return
	}
	if err := u.startWatch(prev, update.PendingPath(u.dir)); err != nil {
		u.h.logf("update: watchdog: %v (the new version still checks itself)", err)
	}
	u.set("restarting", "", "")
	u.h.logf("update: restarting into %s", m.Version)
	time.Sleep(500 * time.Millisecond) // the apps get the restarting event before the socket goes
	if err := u.exec(p.RunPath()); err != nil {
		_ = update.RestoreAll(p)
		_ = os.Remove(update.PendingPath(u.dir))
		fail(err)
	}
}

// stagedDir is where the update to build downloads its files: the hub's binary, the node binaries in nodes/ and the
// programs in programs/ (docs/operations/hub-update.md "Updating").
func (u *updater) stagedDir(build int64) string {
	return filepath.Join(u.dir, strconv.FormatInt(build, 10))
}

// removeDownloads removes an update's downloads once nothing reads them any more: after the update failed, was
// confirmed (its node binaries are in NodeBinaryDir by then) or went back. Each one is tens to hundreds of MB.
func (u *updater) removeDownloads(staged string) {
	if err := os.RemoveAll(staged); err != nil {
		u.h.logf("update: remove the downloads in %s: %v", staged, err)
	}
}

// pruneDownloads removes the downloads of every update but the one to build keep (0: none) when the hub starts: one
// that went back through the watchdog or the edge (both start the old build, which never knew of it), one a stop
// cut short, and those an older build left. Only <build>/ directories go; the markers and logs beside them stay.
func (u *updater) pruneDownloads(keep int64) {
	entries, err := os.ReadDir(u.dir)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			u.h.logf("update: list the downloads: %v", err)
		}
		return
	}
	for _, e := range entries {
		build, err := strconv.ParseInt(e.Name(), 10, 64)
		if !e.IsDir() || err != nil || build <= 0 || build == keep || strconv.FormatInt(build, 10) != e.Name() {
			continue
		}
		u.removeDownloads(filepath.Join(u.dir, e.Name()))
	}
}

// nodePlatforms are the platforms the release publishes a node binary for, sorted. That is every platform in the
// manifest, not only the ones the machines the hub knows now run: a machine added later, or one that had not connected
// when the hub updated, is deployed to from these files (internal/devices nodeBinary), and nothing else fetches its
// platform until the next update.
func nodePlatforms(m *update.Manifest) []string {
	out := make([]string, 0, len(m.Files))
	for plat := range m.Files {
		out = append(out, plat)
	}
	slices.Sort(out)
	return out
}

func (u *updater) onlineIDs() []string {
	var out []string
	for _, m := range u.h.machinesNow() {
		m.mu.Lock()
		if m.online {
			out = append(out, m.cfg.ID)
		}
		m.mu.Unlock()
	}
	return out
}

func (u *updater) healthURL() string {
	host, port, err := net.SplitHostPort(u.h.cfg.Listen)
	if err != nil || port == "" {
		port = "18660"
	}
	if host == "" || host == "0.0.0.0" || host == "::" {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port) + "/api/v1/health"
}

// updateBusy is what a restart would interrupt: any conversation working or waiting for the user (the assistant's
// and every agent's), and a machine being redeployed.
func (h *Hub) updateBusy() []busyItem {
	var out []busyItem
	for _, m := range h.machinesNow() {
		if m.snapshot().Deploying {
			out = append(out, busyItem{MachineID: m.cfg.ID, Machine: m.name(), Title: su(h.userLang(), "busy_redeploying")})
		}
		m.mu.Lock()
		resident := m.info.NodeUpdate // advertised only by a resident supervisor
		m.mu.Unlock()
		list := m.keptSessions() // only read, and no copy of it: the manager stamp is not read here
		for i := range list {
			s := &list[i]
			// Terminal-host sessions outlive the node transport. Unknown hosts and
			// direct assistant processes still wait: an ephemeral SSH node may own them.
			if h.slot != nil && !h.upd.nodesEnabled() && m.cfg.Kind != "local" &&
				(resident || s.Host == proto.EngineHerdr || s.Host == proto.EngineHerdrCore) {
				continue
			}
			// On a turn, not working on in the background (#2228): a server an agent left running would hold the
			// update back for as long as it runs, and the restart does not stop it.
			if s.Status == proto.StatusWorking || s.State == proto.StateNeedsYou {
				out = append(out, busyItem{MachineID: m.cfg.ID, Machine: m.name(), SessionID: s.SessionID, Title: agentTitle(*s)})
			}
		}
	}
	return out
}

// agentTitle is an agent's title for the update's busy list.
func agentTitle(s proto.Session) string {
	t := transcript.Title(s.Title)
	if t == "" || t == "Claude Code" {
		t = path.Base(s.Cwd)
	}
	return clip(oneLine(t), 24)
}
