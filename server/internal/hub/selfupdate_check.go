package hub

import (
	"context"
	"errors"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/update"
)

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

// wantsAuto: on, newer, not a bad build, its code contains this one's (Build.Hold), it leaves no program a service
// runs as it was (unreplaced), not already installing or confirming.
func (u *updater) wantsAuto() bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	r := u.release
	return (u.loaded || u.h.store == nil) && u.settings.auto() && !u.updateActiveLocked() && r != nil && u.h.build.Newer(r.Manifest.Build) &&
		!slices.Contains(u.settings.Bad, r.Manifest.Build) && u.h.build.Hold(r.Manifest) == "" && u.blocked == nil
}

// noteHeld writes into the history, once per release, that a newer release was not installed by itself because its
// code may not contain this hub's (Build.Hold): the app shows it as available, with the reason, and the user can
// still install it. Or because it would leave a program a service runs as it was (unreplaced): installing it by hand
// is refused for the same reason.
func (u *updater) noteHeld(ctx context.Context) {
	if err := u.load(ctx); err != nil {
		return
	}
	u.mu.Lock()
	r := u.release
	if !u.settings.auto() || u.updateActiveLocked() || r == nil || !u.h.build.Newer(r.Manifest.Build) || slices.Contains(u.settings.Bad, r.Manifest.Build) {
		u.mu.Unlock()
		return
	}
	why := u.h.build.Hold(r.Manifest)
	if why == "" && u.blocked != nil {
		why = i18n.Localize(u.blocked, u.h.userLang())
	}
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
	u.setCheckState("checking", "")
	cctx, cancel := context.WithTimeout(ctx, updateRequestLimit)
	defer cancel()
	r, err := update.Check(cctx, u.client, u.h.build.UpdateURL)
	var blocked error
	if err == nil {
		blocked = u.unreplaced(r.Manifest)
	}
	u.mu.Lock()
	u.lastCheck = time.Now()
	if err == nil {
		u.release, u.releaseRead, u.blocked = r, u.lastCheck, blocked
	}
	u.mu.Unlock()
	if err != nil {
		u.setCheckState("idle", su(u.h.userLang(), "check_failed", i18n.Args{"error": i18n.Localize(err, u.h.userLang())}))
		return nil, err
	}
	if u.h.build.Newer(r.Manifest.Build) {
		u.setCheckState("available", "")
	} else {
		u.setCheckState("idle", "")
	}
	return r, nil
}

// updateActiveLocked includes the new process, where boot clears confirmed before confirm starts and running is false.
// Called with u.mu held.
func (u *updater) updateActiveLocked() bool {
	return u.running || !u.confirmed || u.state == "confirming"
}

// setCheckState leaves an install's state, progress and error alone, but still broadcasts refreshed metadata.
// Check under the same lock as the write, both before and after the request: an install can start while it is in flight.
func (u *updater) setCheckState(state, errText string) {
	u.mu.Lock()
	if !u.updateActiveLocked() {
		u.state, u.progress, u.errText = state, "", errText
	}
	u.mu.Unlock()
	u.h.broadcast(map[string]any{"type": "hub_update", "update": u.view()})
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
	blocked := u.unreplaced(r.Manifest)
	u.mu.Lock()
	u.lastCheck = time.Now()
	u.release, u.releaseRead, u.blocked = r, u.lastCheck, blocked
	available := u.state == "idle" && u.h.build.Newer(r.Manifest.Build)
	u.mu.Unlock()
	if available {
		u.setState("available")
	}
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
