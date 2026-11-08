package hub

import (
	"context"
	"errors"
	"fmt"
	"github.com/nwverse/persona/server/internal/compat"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/osproc"

	"github.com/nwverse/persona/server/internal/hubedge"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
	"github.com/nwverse/persona/server/internal/update"
)

// The hub updating itself to the publisher's release (docs/operations/hub-update.md"Updates"): check the manifest every hour, download and verify, wait until the assistant and every agent are idle, swap the binary (keeping the old
// one), restart into the new one, and count it a success only once the machines that were connected are back;
// otherwise go back to the old binary, by the new process itself or by the old one's watchdog. A build that went
// back is not tried again automatically. Then the nodes follow, through deploy.go.

const (
	updateCheckEvery   = time.Hour // docs/operations/hub-update.md "Checking for updates" (hourly since 2026-11-07)
	updateFirstCheck   = time.Minute
	updateConfirmIn    = 2 * time.Minute
	updateWatchFor     = 3 * time.Minute
	updateMaxStarts    = 3
	updateIdleEvery    = 30 * time.Second
	updateManualIdle   = 2 * time.Hour
	updateHistoryKeep  = 50
	updateSettingsKey  = "hub_update"
	updateRequestLimit = 30 * time.Second

	// An install script (GET /i/<code>, managers_install.go) carries the release's sha256s: when the hub last read
	// latest.json longer than releaseFreshFor ago it reads it again first, waiting at most releaseRefreshLimit, and
	// tries that at most once every releaseRefreshEvery whatever the requests. Fresh for no longer than that: someone
	// whose download failed its checksum right after a release gets the new sums by asking again a minute later.
	releaseRefreshEvery = time.Minute
	releaseFreshFor     = releaseRefreshEvery
	releaseRefreshLimit = 5 * time.Second
)

var (
	errNoUpdate    = i18n.Err("hub-selfupdate.err_no_update")
	errNotRelease  = i18n.Err("hub-selfupdate.err_not_release")
	errUpdateBusy  = i18n.Err("hub-selfupdate.err_busy")
	errUpdateAgain = i18n.Err("hub-selfupdate.err_auto_off")
)

// updateRecord is one update in the history (Settings › hub › Update history).
type updateRecord struct {
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	ToBuild     int64     `json:"to_build"`
	Started     time.Time `json:"started"`
	Ended       time.Time `json:"ended"`
	Result      string    `json:"result"` // updated | rolled_back | failed | held (newer, not installed by itself: Reason)
	Reason      string    `json:"reason,omitempty"`
	Auto        bool      `json:"auto"`
}

// updateSettings are kept in Postgres (settings hub_update).
type updateSettings struct {
	Auto           *bool          `json:"auto,omitempty"` // nil: on (the default)
	AutoNoticeSeen bool           `json:"auto_notice_seen"`
	Bad            []int64        `json:"bad,omitempty"` // builds that went back: not installed automatically again
	History        []updateRecord `json:"history,omitempty"`
}

func (s updateSettings) auto() bool { return s.Auto == nil || *s.Auto }

// busyItem is something the restart would interrupt.
type busyItem struct {
	MachineID string `json:"machine_id"`
	Machine   string `json:"machine"`
	SessionID string `json:"session_id,omitempty"`
	Title     string `json:"title"`
}

type updater struct {
	h      *Hub
	dir    string
	client *http.Client
	// Hooks the tests replace: run another binary in this process, start the watchdog, find the running binary,
	// alert the phones.
	exec func(path string) error
	// handover asks the edge to switch to target, the update to build (a slot only, else nil); nil once this slot is
	// released, else why the new one did not come up.
	handover func(ctx context.Context, target string, build int64) error
	// edgeConfirmed tells the edge the update this slot is the result of is confirmed (a slot only, else nil).
	edgeConfirmed func()
	startWatch    func(prev, pendingPath string) error
	target        func() (string, error)
	notify        func(title, body string)
	confirmIn     time.Duration
	idleEvery     time.Duration
	manualIdle    time.Duration

	mu          sync.Mutex
	settings    updateSettings
	loaded      bool
	state       string // idle | checking | available | downloading | waiting_idle | restarting | confirming | updated | rolled_back | failed
	progress    string
	errText     string
	release     *update.Published
	newestImage string // a Docker hub: the newest release tag of its image
	lastCheck   time.Time
	busy        []busyItem
	running     bool
	now         bool // the user confirmed "Update now": stop waiting for idle
	confirmed   bool // no update to confirm, or this process confirmed it

	// refreshing is held by the one request reading latest.json for an install script (freshRelease); refreshTried
	// is when one last did. releaseRead is when release was read: a read that worked (lastCheck is when the hub last
	// tried, which the app shows).
	refreshing   sync.Mutex
	refreshTried time.Time
	releaseRead  time.Time
}

func newUpdater(h *Hub) *updater {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Private publishers may atomically deliver verified releases over SSH.
	// A file source uses the same manifest, checksum and ancestry checks as HTTPS.
	transport.RegisterProtocol("file", http.NewFileTransport(http.Dir("/")))
	u := &updater{h: h, dir: update.Dir(), client: &http.Client{Timeout: 10 * time.Minute, Transport: transport},
		exec: func(path string) error {
			// The sockets systemd passed go on to the new binary (listen.go): it could not listen otherwise.
			// The hub's arguments; `hub …` when it runs as persona-hub and path is personad (update.TargetArgs).
			return execKeepingListeners(path, append([]string{path}, update.TargetArgs(os.Args, path)...), os.Environ())
		},
		startWatch: startWatchdog, target: ServerBinary, confirmIn: updateConfirmIn, idleEvery: updateIdleEvery,
		manualIdle: updateManualIdle, state: "idle", confirmed: true,
		notify: func(title, body string) {
			h.push.send(map[string]any{"aps": map[string]any{
				"alert": map[string]any{"title": title, "body": body}, "sound": "default", "thread-id": "hub_update",
			}}, "hub_update", nil)
		}}
	if h.slot != nil {
		u.exec = u.slotExec
		u.handover = func(ctx context.Context, target string, build int64) error {
			return h.slot.Switch(ctx, target, "", hubedge.KindUpdate, build)
		}
		u.edgeConfirmed = h.slot.Confirmed
	}
	return u
}

// slotExec is how a slot (the edge started it, hub_edge.go) runs the binary at path instead of exec'ing it: once it
// serves, the edge switches to path (rollback: it stops this one); before that (boot) it exits and the edge starts it
// again at once on the binary now at its path.
func (u *updater) slotExec(path string) error {
	if !u.h.slot.Serving() {
		u.h.logf("update: starting again on %s", path)
		os.Exit(hubedge.RespawnCode)
	}
	return u.h.slot.Switch(context.Background(), path, "", hubedge.KindRollback, 0)
}

func runningBinary() (string, error) {
	p, err := os.Executable()
	if err != nil {
		return "", err
	}
	return filepath.EvalSymlinks(p)
}

// ServerBinary is the program that is the whole server: this one, unless it is persona-hub, which runs the hub
// alone: then the personad beside it. What runs or replaces the server goes by it until the hub/manager split is done
// (docs/server/server.md "Programs"): the built-in manager the hub starts (`personad assistant`), the update's target
// (personad is the release's main download; persona-hub is swapped as a program), setup's service binary and the
// edge's default switch.
func ServerBinary() (string, error) {
	p, err := runningBinary()
	if err != nil {
		return "", err
	}
	return serverBinaryOf(p)
}

// serverBinaryOf is ServerBinary for the running binary p.
func serverBinaryOf(p string) (string, error) {
	ext := filepath.Ext(p)
	if strings.TrimSuffix(filepath.Base(p), ext) != "persona-hub" {
		return p, nil
	}
	sibling := filepath.Join(filepath.Dir(p), "personad"+ext)
	if _, err := os.Stat(sibling); err != nil {
		return "", fmt.Errorf("persona-hub runs the hub alone and needs the personad beside it (%s): %w", sibling, err)
	}
	return sibling, nil
}

// startWatchdog runs `<old binary> update-watch <pending>` in its own session, its output in update/watch.log.
func startWatchdog(prev, pendingPath string) error {
	log, err := os.OpenFile(filepath.Join(filepath.Dir(pendingPath), "watch.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer log.Close()
	cmd := exec.Command(prev, "update-watch", pendingPath)
	cmd.Stdout, cmd.Stderr = log, log
	cmd.SysProcAttr = osproc.DetachedAttr()
	if err := cmd.Start(); err != nil {
		return err
	}
	return cmd.Process.Release()
}

// boot runs once, first thing when the hub starts, before the config or the database: a new version that keeps
// dying (in a service manager's restart loop) goes back after updateMaxStarts starts; an old version that finds a
// marker for an update it is not (the watchdog or a person put it back) clears it for recording. It says whether
// this process has an update of its own to confirm.
func (u *updater) boot() (pending bool) {
	var p update.Pending
	ok, err := update.Load(update.PendingPath(u.dir), &p)
	if err != nil || !ok {
		return false
	}
	if p.ToBuild != u.h.build.Number {
		if u.h.build.Number == p.FromBuild {
			u.markRolledBack(p, su(u.h.userLang(), "reason_went_back"))
		} else {
			_ = os.Remove(update.PendingPath(u.dir)) // replaced by a build deployed straight from master: not a failure
		}
		return false
	}
	u.confirmed = false
	p.Starts++
	if err := update.Save(update.PendingPath(u.dir), p); err != nil {
		// Without the count on disk every restart reads the same Starts, so a build that keeps crashing would never
		// reach updateMaxStarts: go back to the version that ran before while this process still can.
		u.h.logf("update: save start count: %v", err)
		u.rollback(p, su(u.h.userLang(), "reason_marker_unsaved"))
		return true
	}
	if p.Starts > updateMaxStarts {
		u.rollback(p, su(u.h.userLang(), "reason_no_start", i18n.Args{"count": p.Starts - 1}))
	}
	return true
}

// start loads the settings, records an update that went back, confirms one under way, and checks for releases.
func (u *updater) start(ctx context.Context) {
	u.load(ctx)
	u.recordRolledBack(ctx)
	var p update.Pending
	if ok, _ := update.Load(update.PendingPath(u.dir), &p); ok && p.ToBuild == u.h.build.Number {
		go u.confirm(ctx, p)
	}
	go u.loop(ctx)
}

func (u *updater) load(ctx context.Context) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.loadLocked(ctx)
}

// loadLocked publishes settings only after a complete read, so a failed read can be retried.
func (u *updater) loadLocked(ctx context.Context) error {
	if u.loaded {
		return nil
	}
	if u.h.store != nil {
		var settings updateSettings
		if _, err := u.h.store.Setting(ctx, updateSettingsKey, &settings); err != nil {
			u.h.logf("update: settings: %v", err)
			return err
		}
		u.settings = settings
	}
	u.loaded = true
	return nil
}

// saveLocked writes the settings (called with u.mu held after a successful load).
func (u *updater) saveLocked(ctx context.Context) error {
	if err := u.loadLocked(ctx); err != nil {
		return err
	}
	if len(u.settings.History) > updateHistoryKeep {
		u.settings.History = u.settings.History[len(u.settings.History)-updateHistoryKeep:]
	}
	if u.h.store != nil {
		if err := u.h.store.PutSetting(ctx, updateSettingsKey, u.settings); err != nil {
			u.h.logf("update: save settings: %v", err)
			u.loaded = false // The next attempt must start from the persisted settings.
			return err
		}
	}
	return nil
}

func (u *updater) record(ctx context.Context, r updateRecord) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.loadLocked(ctx); err != nil {
		return err
	}
	u.settings.History = append(u.settings.History, r)
	if r.Result == "rolled_back" && !slices.Contains(u.settings.Bad, r.ToBuild) {
		u.settings.Bad = append(u.settings.Bad, r.ToBuild)
	}
	return u.saveLocked(ctx)
}

func (u *updater) markRolledBack(p update.Pending, reason string) {
	path := update.RolledBackPath(u.dir)
	if _, err := os.Stat(path); err != nil {
		if err := update.Save(path, update.RolledBack{FromVersion: p.FromVersion, ToVersion: p.ToVersion, ToBuild: p.ToBuild,
			Started: p.Started, At: time.Now(), Reason: reason, Auto: p.Auto}); err != nil {
			u.h.logf("update: save rollback marker (history and bad builds miss it): %v", err)
		}
	}
	if err := os.Remove(update.PendingPath(u.dir)); err != nil && !errors.Is(err, os.ErrNotExist) {
		u.h.logf("update: remove pending marker: %v", err)
	}
}

// recordRolledBack puts an update that went back into the history and the bad builds, once the database is there.
func (u *updater) recordRolledBack(ctx context.Context) {
	var rb update.RolledBack
	if ok, err := update.Load(update.RolledBackPath(u.dir), &rb); err != nil || !ok {
		return
	}
	if err := u.record(ctx, updateRecord{FromVersion: rb.FromVersion, ToVersion: rb.ToVersion, ToBuild: rb.ToBuild, Started: rb.Started,
		Ended: rb.At, Result: "rolled_back", Reason: rb.Reason, Auto: rb.Auto}); err != nil {
		return // Keep the marker until the history and bad-build list have been saved.
	}
	l := u.h.userLang()
	why := su(l, "rolled_back", i18n.Args{"to": rb.ToVersion, "from": u.h.version, "reason": rb.Reason})
	u.set("rolled_back", "", why)
	_ = os.Remove(update.RolledBackPath(u.dir))
	u.notify(su(l, "push_failed_title"), su(l, "push_failed_body", i18n.Args{"why": why}))
	u.h.logf("update: %s went back to %s: %s", rb.ToVersion, u.h.version, rb.Reason)
}

// confirm decides whether the update this process is the result of worked: the database answers and every machine
// connected before the restart is back within confirmIn. Then the nodes follow; otherwise back to the old binary.
func (u *updater) confirm(ctx context.Context, p update.Pending) {
	u.set("confirming", "", "")
	deadline := time.Now().Add(u.confirmIn)
	missing := p.Online
	for {
		missing = u.offline(p.Online)
		dbOK := u.h.store == nil
		if !dbOK {
			var x updateSettings
			_, err := u.h.store.Setting(ctx, updateSettingsKey, &x)
			dbOK = err == nil
		}
		if dbOK && len(missing) == 0 {
			break
		}
		if !time.Now().Before(deadline) {
			l := u.h.userLang()
			why := su(l, "reason_no_database")
			if dbOK {
				why = su(l, "reason_machines_missing", i18n.Args{"machines": strings.Join(missing, su(l, "list_sep"))})
			}
			u.rollback(p, why)
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
	_ = os.Remove(update.PendingPath(u.dir))
	u.mu.Lock()
	u.confirmed = true
	u.mu.Unlock()
	u.record(ctx, updateRecord{FromVersion: p.FromVersion, ToVersion: p.ToVersion, ToBuild: p.ToBuild, Started: p.Started,
		Ended: time.Now(), Result: "updated", Auto: p.Auto})
	u.set("updated", "", "")
	u.h.logf("update: %s -> %s confirmed", p.FromVersion, p.ToVersion)
	if p.Auto { // one the user started is watched in the app
		l := u.h.userLang()
		u.notify(su(l, "push_updated_title", i18n.Args{"version": p.ToVersion}), su(l, "push_updated_body"))
	}
	if u.edgeConfirmed != nil {
		u.edgeConfirmed() // the edge's own check of this update ends (it would go back after 3 minutes)
	}
	u.updateNodes(ctx, p.Nodes)
}

// offline lists which of ids are not connected.
func (u *updater) offline(ids []string) []string {
	var out []string
	for _, id := range ids {
		m := u.h.localMachine(id)
		if m == nil {
			continue // gone from the config: nothing to wait for
		}
		m.mu.Lock()
		on := m.online
		m.mu.Unlock()
		if !on {
			out = append(out, id)
		}
	}
	return out
}

// rollback writes why, puts the old binary back and runs it. It returns only when that fails.
func (u *updater) rollback(p update.Pending, reason string) {
	u.h.logf("update: going back to %s: %s", p.FromVersion, reason)
	u.markRolledBack(p, reason)
	if err := update.RestoreAll(p); err != nil {
		u.h.logf("update: restore %s: %v", p.Prev, err)
		return
	}
	if err := u.exec(p.Target); err != nil {
		u.h.logf("update: run %s: %v", p.Target, err)
	}
}

// updateNodes puts the release's node binaries where "Redeploy" takes them (the manager's, internal/devices deploy.go)
// and has the manager redeploy each connected machine whose node is not this build, one at a time.
func (u *updater) updateNodes(ctx context.Context, staged string) {
	if !u.nodesEnabled() {
		return
	}
	if staged != "" {
		files, _ := filepath.Glob(filepath.Join(staged, "personad-*"))
		legacy, _ := filepath.Glob(filepath.Join(staged, compat.DaemonBinary+"-*"))
		files = append(files, legacy...)
		for _, f := range files {
			if strings.HasSuffix(f, ".gz") || strings.HasSuffix(f, ".part") {
				continue
			}
			if err := os.MkdirAll(identity.NodeBinaryDir(), 0o755); err == nil {
				_, err = update.Swap(filepath.Join(identity.NodeBinaryDir(), filepath.Base(f)), f)
				if err != nil && errors.Is(err, os.ErrNotExist) {
					err = copyInto(f, filepath.Join(identity.NodeBinaryDir(), filepath.Base(f)))
				}
				if err != nil {
					u.h.logf("update: node binary %s: %v", filepath.Base(f), err)
				} else {
					// It is this build now: a version file put beside an older one (docs/operations/docker.md "Node builds") goes.
					_ = os.Remove(filepath.Join(identity.NodeBinaryDir(), filepath.Base(f)+".version"))
				}
			}
		}
	}
	u.h.updateNodes(ctx) // the manager redeploys them, one at a time (internal/devices deploy.go)
}

func copyInto(from, to string) error {
	b, err := os.ReadFile(from)
	if err != nil {
		return err
	}
	return os.WriteFile(to, b, 0o755)
}

// loop checks for releases (a minute after starting, then every hour) and installs one when updating itself is
// on and the release is newer and not a build that went back.
func (u *updater) loop(ctx context.Context) {
	if !u.h.build.SelfUpdates() && u.h.build.Channel != update.Docker {
		return
	}
	wait := updateFirstCheck
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		wait = updateCheckEvery
		if _, err := u.check(ctx); err != nil {
			continue
		}
		if u.wantsAuto() {
			go u.install(ctx, true)
		} else {
			u.noteHeld(ctx)
		}
	}
}

// wantsAuto: on, newer, not a bad build, its code contains this one's (Build.Hold), not already running.
func (u *updater) wantsAuto() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	r := u.release
	return (u.loaded || u.h.store == nil) && u.settings.auto() && !u.running && r != nil && u.h.build.Newer(r.Manifest.Build) &&
		!slices.Contains(u.settings.Bad, r.Manifest.Build) && u.h.build.Hold(r.Manifest) == ""
}

// noteHeld writes into the history, once per release, that a newer release was not installed by itself because its
// code may not contain this hub's (Build.Hold): the app shows it as available, with the reason, and the user can
// still install it.
func (u *updater) noteHeld(ctx context.Context) {
	if err := u.load(ctx); err != nil {
		return
	}
	u.mu.Lock()
	r := u.release
	if !u.settings.auto() || u.running || r == nil || !u.h.build.Newer(r.Manifest.Build) || slices.Contains(u.settings.Bad, r.Manifest.Build) {
		u.mu.Unlock()
		return
	}
	why := u.h.build.Hold(r.Manifest)
	for _, h := range u.settings.History {
		if h.Result == "held" && h.ToBuild == r.Manifest.Build {
			why = ""
		}
	}
	u.mu.Unlock()
	if why == "" {
		return
	}
	u.h.logf("update: %s is newer but not installed by itself: %s", r.Manifest.Version, why)
	now := time.Now()
	u.record(ctx, updateRecord{FromVersion: u.h.version, ToVersion: r.Manifest.Version, ToBuild: r.Manifest.Build,
		Started: now, Ended: now, Result: "held", Reason: why, Auto: true})
}

// check reads the release now (a Docker hub: its image's tags).
func (u *updater) check(ctx context.Context) (*update.Published, error) {
	if err := u.load(ctx); err != nil {
		return nil, err
	}
	u.recordRolledBack(ctx)
	if u.h.build.Channel == update.Docker {
		return nil, u.checkImage(ctx)
	}
	if u.h.build.UpdateURL == "" {
		return nil, errNotRelease
	}
	u.setState("checking")
	cctx, cancel := context.WithTimeout(ctx, updateRequestLimit)
	defer cancel()
	r, err := update.Check(cctx, u.client, u.h.build.UpdateURL)
	u.mu.Lock()
	u.lastCheck = time.Now()
	if err == nil {
		u.release, u.releaseRead = r, u.lastCheck
	}
	u.mu.Unlock()
	if err != nil {
		u.set("idle", "", su(u.h.userLang(), "check_failed", i18n.Args{"error": i18n.Localize(err, u.h.userLang())}))
		return nil, err
	}
	if u.h.build.Newer(r.Manifest.Build) {
		u.set("available", "", "")
	} else {
		u.set("idle", "", "")
	}
	return r, nil
}

// freshRelease reads latest.json again for an install script when the release the hub has is older than
// releaseFreshFor: the script checks the download (the newest one on the site) against its sha256s, so sums from
// before the last release would fail every install until the next check. Bounded by releaseRefreshLimit and tried at
// most once every releaseRefreshEvery across requests; when it fails the release the hub had stays. Nothing for a
// Docker hub or a build with nowhere to look.
func (u *updater) freshRelease(ctx context.Context) {
	if u == nil || u.h.build.Channel == update.Docker || u.h.build.UpdateURL == "" {
		return
	}
	u.refreshing.Lock()
	defer u.refreshing.Unlock()
	now := time.Now()
	u.mu.Lock()
	stale := now.Sub(u.releaseRead) >= releaseFreshFor && now.Sub(u.refreshTried) >= releaseRefreshEvery
	if stale {
		u.refreshTried = now
	}
	u.mu.Unlock()
	if !stale {
		return
	}
	cctx, cancel := context.WithTimeout(ctx, releaseRefreshLimit)
	defer cancel()
	r, err := update.Check(cctx, u.client, u.h.build.UpdateURL)
	if err != nil {
		u.h.logf("update: reading the release for an install script: %v (keeping the one read before)", err)
		return
	}
	u.mu.Lock()
	u.lastCheck = time.Now()
	u.release, u.releaseRead = r, u.lastCheck
	available := u.state == "idle" && u.h.build.Newer(r.Manifest.Build)
	u.mu.Unlock()
	if available {
		u.setState("available")
	}
}

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
	u.mu.Lock()
	u.now = now
	u.mu.Unlock()
	go u.install(ctx, false)
	return nil
}

// whyNot is why this hub cannot update itself (begin's error); whyNotReason is the reason alone (the view's why_not).
func (u *updater) whyNot() error {
	key, args := u.whyNotKey()
	return i18n.Err("hub-selfupdate.not_release_"+key, args)
}

func (u *updater) whyNotReason(l i18n.Lang) string {
	key, args := u.whyNotKey()
	return su(l, "why_"+key, args)
}

func (u *updater) whyNotKey() (string, i18n.Args) {
	switch u.h.build.Channel {
	case update.Docker:
		return "docker", nil
	case update.Release:
		return "no_source", nil
	}
	return "dev", i18n.Args{"version": u.h.version}
}

// su is a hub-selfupdate text in l.
func su(l i18n.Lang, name string, args ...i18n.Args) string {
	return i18n.T(l, "hub-selfupdate."+name, args...)
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
	fail := func(err error) {
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
	staged := filepath.Join(u.dir, fmt.Sprint(m.Build))
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
	// The programs the hub runs beside it (personad-wechat): this platform's build of each, checked like personad.
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
		if err := u.handover(ctx, target, m.Build); err != nil {
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
	if err := u.exec(target); err != nil {
		_ = update.RestoreAll(p)
		_ = os.Remove(update.PendingPath(u.dir))
		fail(err)
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
		for _, s := range h.decorated(m) {
			// Terminal-host sessions outlive the node transport. Unknown hosts and
			// direct assistant processes still wait: an ephemeral SSH node may own them.
			if h.slot != nil && !h.upd.nodesEnabled() && m.cfg.Kind != "local" &&
				(resident || s.Host == proto.EngineHerdr || s.Host == proto.EngineHerdrCore) {
				continue
			}
			// On a turn, not working on in the background (#2228): a server an agent left running would hold the
			// update back for as long as it runs, and the restart does not stop it.
			if s.Status == proto.StatusWorking || s.State == proto.StateNeedsYou {
				out = append(out, busyItem{MachineID: m.cfg.ID, Machine: m.name(), SessionID: s.SessionID, Title: agentTitle(s)})
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

func (u *updater) setState(state string) { u.set(state, "", "") }

func (u *updater) isConfirmed() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.confirmed
}

// set changes the state the apps see and tells them.
func (u *updater) set(state, progress, errText string) {
	u.mu.Lock()
	u.state, u.progress = state, progress
	if errText != "" || state != "rolled_back" {
		u.errText = errText
	}
	u.mu.Unlock()
	u.h.broadcast(map[string]any{"type": "hub_update", "update": u.view()})
}

// updateView is GET /api/v1/hub/update.
type updateView struct {
	Version    string      `json:"version"`
	Build      int64       `json:"build"`
	Channel    string      `json:"channel"`
	CanUpdate  bool        `json:"can_update"`
	Why        string      `json:"why_not,omitempty"`
	Latest     *latestView `json:"latest,omitempty"`
	State      string      `json:"state"`
	Progress   string      `json:"progress,omitempty"`
	Busy       []busyItem  `json:"busy"`
	Error      string      `json:"error,omitempty"`
	Auto       bool        `json:"auto"`
	AutoNotice bool        `json:"auto_notice"`
	LastCheck  *time.Time  `json:"last_check,omitempty"`
	Confirmed  bool        `json:"confirmed"`
	Image      *imageView  `json:"image,omitempty"` // a Docker hub's image
}

type latestView struct {
	Version  string     `json:"version"`
	Build    int64      `json:"build"` // 0 for an image (tags have no build numbers)
	Released *time.Time `json:"released,omitempty"`
	Notes    string     `json:"notes,omitempty"`
	Newer    bool       `json:"newer"`
	Bad      bool       `json:"bad"` // went back before: not installed automatically
	// Held: why a newer release is not installed automatically even with auto-update on (Build.Hold), for people.
	Held string `json:"held,omitempty"`
}

func (u *updater) view() updateView { return u.viewIn(u.h.userLang()) }

// viewIn is the view with its texts for people in l.
func (u *updater) viewIn(l i18n.Lang) updateView {
	u.mu.Lock()
	defer u.mu.Unlock()
	v := updateView{Version: u.h.version, Build: u.h.build.Number, Channel: u.h.build.Channel, CanUpdate: u.h.build.SelfUpdates(),
		State: u.state, Progress: u.progress, Busy: u.busy, Error: u.errText, Auto: u.settings.auto() && u.h.build.SelfUpdates(),
		AutoNotice: u.h.build.SelfUpdates() && !u.settings.AutoNoticeSeen, Confirmed: u.confirmed}
	if v.Busy == nil {
		v.Busy = []busyItem{}
	}
	if !v.CanUpdate {
		v.Why = u.whyNotReason(l)
	}
	if !u.lastCheck.IsZero() {
		t := u.lastCheck
		v.LastCheck = &t
	}
	if r := u.release; r != nil {
		released := r.Manifest.Released
		v.Latest = &latestView{Version: r.Manifest.Version, Build: r.Manifest.Build, Released: &released, Notes: r.Manifest.Notes,
			Newer: u.h.build.Newer(r.Manifest.Build), Bad: slices.Contains(u.settings.Bad, r.Manifest.Build)}
		if v.Latest.Newer && u.h.build.SelfUpdates() {
			v.Latest.Held = u.h.build.Hold(r.Manifest)
		}
	}
	if u.h.build.Channel == update.Docker {
		if im, err := u.dockerImage(); err == nil {
			v.Image = &imageView{Name: im.Name(), Tag: im.Tag, Pinned: im.Pinned()}
		}
		if u.newestImage != "" {
			v.Latest = &latestView{Version: u.newestImage, Newer: update.NewerVersion(u.newestImage, u.h.version)}
		}
	}
	return v
}

// imageView is the image a Docker hub runs, for the app's instructions: a pinned tag has to be changed in .env.
type imageView struct {
	Name   string `json:"name"`
	Tag    string `json:"tag"`
	Pinned bool   `json:"pinned"`
}

// dockerImage is the image this Docker hub runs: the compose file's PERSONA_IMAGE, else the one it was built as.
func (u *updater) dockerImage() (update.Image, error) {
	s := identity.Getenv("PERSONA_IMAGE")
	if s == "" {
		s = u.h.build.Image
	}
	if s == "" {
		return update.Image{}, i18n.Err("hub-selfupdate.err_no_image")
	}
	return update.ParseImage(s)
}

// checkImage asks the registry for the newest release of the image this Docker hub runs (docs/operations/hub-update.md "Docker").
// A private image needs PERSONA_REGISTRY_TOKEN (a read token) in the container's environment.
func (u *updater) checkImage(ctx context.Context) error {
	im, err := u.dockerImage()
	if err != nil {
		u.set("idle", "", i18n.Localize(err, u.h.userLang()))
		return err
	}
	u.setState("checking")
	cctx, cancel := context.WithTimeout(ctx, updateRequestLimit)
	defer cancel()
	newest, err := update.LatestVersion(cctx, u.client, im, identity.Getenv("PERSONA_REGISTRY_TOKEN"))
	u.mu.Lock()
	u.lastCheck = time.Now()
	if err == nil {
		u.newestImage = newest
	}
	u.mu.Unlock()
	switch {
	case errors.Is(err, update.ErrPrivate):
		u.set("idle", "", su(u.h.userLang(), "image_private", i18n.Args{"error": err.Error()}))
	case err != nil:
		u.set("idle", "", su(u.h.userLang(), "image_failed", i18n.Args{"error": err.Error()}))
	case update.NewerVersion(newest, u.h.version):
		u.set("available", "", "")
	default:
		u.set("idle", "", "")
	}
	return err
}

func (u *updater) routes(api *http.ServeMux) {
	api.HandleFunc("GET /api/v1/hub/update", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("POST /api/v1/hub/update/check", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if _, err := u.check(r.Context()); err != nil && !errors.Is(err, errNotRelease) {
			writeErr(w, http.StatusBadGateway, err)
			return
		}
		if u.wantsAuto() {
			go u.install(context.WithoutCancel(r.Context()), true)
		} else {
			u.noteHeld(r.Context())
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("POST /api/v1/hub/update", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		var body struct {
			Now bool `json:"now"`
		}
		// No body (or one that does not decode) is an ordinary update; only an oversized one is refused.
		if err := decodeJSONBody(w, r, &body, 4<<10); bodyTooLarge(err) {
			writeBodyErr(w, err, err)
			return
		}
		// The update outlives this request: it runs on the hub's own context.
		if err := u.begin(context.WithoutCancel(r.Context()), body.Now); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("PUT /api/v1/hub/update/settings", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		var body struct {
			Auto           *bool `json:"auto"`
			AutoNoticeSeen *bool `json:"auto_notice_seen"`
		}
		if err := decodeJSONBody(w, r, &body, 4<<10); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		u.mu.Lock()
		if err := u.loadLocked(r.Context()); err != nil {
			u.mu.Unlock()
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		if body.Auto != nil {
			a := *body.Auto
			u.settings.Auto = &a
			u.settings.AutoNoticeSeen = true // they chose: no need to tell them again
		}
		if body.AutoNoticeSeen != nil {
			u.settings.AutoNoticeSeen = *body.AutoNoticeSeen
		}
		err := u.saveLocked(r.Context())
		u.mu.Unlock()
		if err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		writeJSON(w, u.viewIn(u.h.requestLang(r)))
	})
	api.HandleFunc("GET /api/v1/hub/update/history", func(w http.ResponseWriter, r *http.Request) {
		if err := u.load(r.Context()); err != nil {
			writeErr(w, http.StatusServiceUnavailable, i18n.Err("http-status.unavailable"))
			return
		}
		u.mu.Lock()
		out := slices.Clone(u.settings.History)
		u.mu.Unlock()
		slices.Reverse(out)
		if out == nil {
			out = []updateRecord{}
		}
		writeJSON(w, map[string]any{"history": out})
	})
}
