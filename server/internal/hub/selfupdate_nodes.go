package hub

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"

	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/update"
)

func (u *updater) nodesEnabled() bool {
	return u.h.cfg != nil && u.h.cfg.AutoUpdateNodes
}

// stageNodes never fetches or recreates the node package directory unless fleet
// updates are explicitly enabled. Hub-only releases can contain just one platform.
func (u *updater) stageNodes(ctx context.Context, m *update.Manifest, staged, bin string) (string, error) {
	if !u.nodesEnabled() {
		return "", nil
	}
	nodes := filepath.Join(staged, "nodes")
	own := runtime.GOOS + "-" + runtime.GOARCH
	for _, plat := range nodePlatforms(m) {
		u.set("downloading", plat, "")
		if plat == own {
			// This platform's node binary is the hub's own binary: the same file, so copy the one already here
			// rather than fetching it twice (a machine on the hub's platform added later needs it too).
			err := os.MkdirAll(nodes, 0o755)
			if err == nil {
				err = copyInto(bin, filepath.Join(nodes, "personad-"+plat))
				if err == nil {
					err = copyInto(bin, filepath.Join(nodes, compat.DaemonBinary+"-"+plat))
				}
			}
			if err != nil {
				return "", fmt.Errorf("the node binary for the hub's own platform: %w", err)
			}
			continue
		}
		if _, err := update.Download(ctx, u.client, u.h.build.UpdateURL, m.Files[plat], nodes); err != nil {
			return "", err
		}
	}
	return nodes, nil
}

// nodeBuildName: a personad build for one platform as bin/nodes holds it (personad-<os>-<arch>, or the former name).
var nodeBuildName = lazyre.New(`^(?:personad|` + regexp.QuoteMeta(compat.DaemonBinary) + `)-[a-z0-9]+-[a-z0-9]+$`)

// setAsideOldNodes renames the personad builds in dir that would pass for the hub's own version and no longer are. The
// manager takes a personad there with no version file beside it for a build of the hub's version
// (internal/devices/packages.go), which stops being true once the hub updates itself without its nodes: Redeploy would
// put the old build back on every computer (#1825). Set aside as <name>.prev, the manager fetches the hub's own build
// from the release instead. A build with a version file says what it is and stays; the directory is never created. It
// returns the names it moved.
func setAsideOldNodes(dir string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var moved []string
	for _, e := range entries {
		name := e.Name()
		if !e.Type().IsRegular() || !nodeBuildName.MatchString(name) {
			continue
		}
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path + ".version"); err == nil {
			continue
		}
		if err := os.Rename(path, path+".prev"); err != nil {
			continue
		}
		moved = append(moved, name)
	}
	return moved
}
