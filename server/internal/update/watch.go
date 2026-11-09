package update

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

// Health is what the watchdog reads from the hub's /api/v1/health.
type Health struct {
	Build  int64 `json:"build"`
	Update struct {
		Confirmed bool `json:"confirmed"` // this process finished an update's checks (or had none to do)
	} `json:"update"`
}

// Watch is the watchdog `personad update-watch` runs: the old binary's code, started just before the hub runs the new
// one. It waits until the hub answers as the new build with the update confirmed; by deadline it has not (the new
// version hangs, or dies before it can roll back by itself), it restores the old binary, writes why, and restarts
// the hub on it. Someone else finishing the update (the pending marker gone) ends it too, also while the last health
// check was still out: before going back it claims the marker with a rename, and a confirm that finds the marker
// claimed leaves the update to this watchdog (hub/selfupdate.go confirm). A claim left behind by a watchdog that died
// goes back at the hub's next start (Unclaim).
func Watch(ctx context.Context, client *http.Client, p Pending, pendingPath string, deadline time.Time, every time.Duration,
	restore func() error, restart func() error) error {
	for {
		if _, err := os.Stat(pendingPath); os.IsNotExist(err) {
			return nil // confirmed, or rolled back by the new version itself
		}
		if h, err := health(ctx, client, p.HealthURL); err == nil {
			if h.Build == p.ToBuild && h.Update.Confirmed {
				return nil
			}
			if h.Build != 0 && h.Build != p.ToBuild && h.Build != p.FromBuild {
				return nil // a build deployed straight from master took over meanwhile: not this watchdog's to undo
			}
		}
		if !time.Now().Before(deadline) {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(every):
		}
	}
	marker := claimedPath(pendingPath)
	if err := os.Rename(pendingPath, marker); errors.Is(err, os.ErrNotExist) {
		return nil // confirmed (or rolled back) while the last health check was out
	} else if err != nil {
		// The marker cannot move (an update directory that is read-only or has the wrong owner): go back without the
		// claim, as the watchdog did before claims. Such a directory does not let a confirm remove the marker either.
		marker = pendingPath
	}
	if err := restore(); err != nil {
		if marker != pendingPath {
			_ = os.Rename(marker, pendingPath) // the update stays under way, as it was
		}
		return err
	}
	_ = Save(RolledBackPath(filepath.Dir(pendingPath)), RolledBack{FromVersion: p.FromVersion, ToVersion: p.ToVersion, ToBuild: p.ToBuild,
		Started: p.Started, At: time.Now(), Reason: "新版本 3 分钟内没有起来并确认（看门狗回滚）", Auto: p.Auto})
	if err := os.Remove(marker); errors.Is(err, os.ErrNotExist) && marker != pendingPath {
		// A hub that started meanwhile put the claim back (Unclaim; launchd restarts a hub that died and leaves this
		// process running): the rollback just written down ends that update as well.
		_ = os.Remove(pendingPath)
	}
	return restart()
}

// claimedPath is where Watch moves the pending marker while it goes back.
func claimedPath(pendingPath string) string { return pendingPath + ".watch" }

// Unclaim puts back a pending marker that a watchdog claimed and never finished with: it died between the claim and
// removing the marker (power loss, or a stop of the hub's unit, whose cgroup it shares under systemd). The hub's next
// start then finds the update as it would have without the claim: the old build records the rollback, the new build
// counts its start and confirms. A pending marker that is there already stays as it is.
func Unclaim(pendingPath string) error {
	if _, err := os.Lstat(pendingPath); !errors.Is(err, os.ErrNotExist) {
		return err // nil: there is a marker
	}
	if err := os.Rename(claimedPath(pendingPath), pendingPath); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

func health(ctx context.Context, client *http.Client, url string) (Health, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var h Health
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return h, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return h, err
	}
	defer resp.Body.Close()
	return h, json.NewDecoder(resp.Body).Decode(&h)
}
