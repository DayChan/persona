package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

// Ready is emitted only after the stores opened and authenticated Hub handshake completed.
type Ready struct {
	ManagerID    string    `json:"manager_id"`
	RunID        string    `json:"run_id"`
	PID          int       `json:"pid"`
	Exe          string    `json:"exe"`
	Source       string    `json:"source"`
	Version      string    `json:"version"`
	NativeSHA256 string    `json:"native_sha256"`
	ConfigSHA256 string    `json:"config_sha256"`
	Connected    bool      `json:"connected"`
	StoresOpened bool      `json:"stores_opened"`
	At           time.Time `json:"at"`
}

func activePath(p Plan) string { return filepath.Join(p.Dir, ".manager-update-active.json") }

// MarkReady is inert outside an active update. It is never called by metadata probes.
func MarkReady(dir string, build update.Build) error {
	path := filepath.Join(dir, ".manager-update-active.json")
	if _, e := os.Lstat(path); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	if e := canonical(path); e != nil {
		return e
	}
	p, e := ReadPlan(path)
	if e != nil {
		return e
	}
	if e = p.validateShape(); e != nil {
		return e
	}
	if p.Dir != dir || build.Commit != p.Source || build.Version != p.Version || build.Channel != update.Release {
		return nil
	}
	exe, e := os.Executable()
	if e != nil {
		return e
	}
	if exe != p.Binary {
		return nil
	}
	h, e := hashFile(exe)
	if e != nil {
		return e
	}
	if h != p.BinarySHA256 {
		return errors.New("update readiness image hash mismatch")
	}
	config, e := hashFile(filepath.Join(dir, "manager.json"))
	if e != nil {
		return e
	}
	if config != p.ConfigSHA256 {
		return errors.New("update changed manager configuration")
	}
	r := Ready{ManagerID: p.ManagerID, RunID: p.RunID, PID: os.Getpid(), Exe: exe, Source: build.Commit, Version: build.Version, NativeSHA256: h, ConfigSHA256: config, Connected: true, StoresOpened: true, At: time.Now().UTC()}
	return writeJSON(filepath.Join(txDir(p), "ready.json"), r)
}
func (c *Controller) liveReady(ctx context.Context, p Plan) (Ready, error) {
	var r Ready
	if e := canonical(filepath.Join(txDir(p), "ready.json")); e != nil {
		return r, e
	}
	b, e := os.ReadFile(filepath.Join(txDir(p), "ready.json"))
	if e != nil {
		return r, e
	}
	if e = json.Unmarshal(b, &r); e != nil {
		return r, e
	}
	if r.RunID != p.RunID || r.ManagerID != p.ManagerID || r.Source != p.Source || r.Version != p.Version || r.NativeSHA256 != p.BinarySHA256 || r.ConfigSHA256 != p.ConfigSHA256 || r.Exe != p.Binary || !r.Connected || !r.StoresOpened || r.PID <= 0 {
		return r, errors.New("manager readiness does not match activation")
	}
	if r.At.After(c.Now()) || c.Now().Sub(r.At) > 90*time.Second {
		return r, errors.New("stale readiness")
	}
	if e = c.checkUnit(ctx, p, true); e != nil {
		return r, e
	}
	b, e = c.Command(ctx, "/bin/launchctl", "print", c.service(p))
	if e != nil {
		return r, e
	}
	if launchFields(string(b))["pid"] != strconv.Itoa(r.PID) {
		return r, errors.New("readiness PID is not the original unit's current process")
	}
	for path, want := range map[string]string{p.Binary: p.BinarySHA256, filepath.Join(p.Dir, "manager.json"): p.ConfigSHA256, p.PlistPath: p.PlistSHA256} {
		h, e := hashFile(path)
		if e != nil {
			return r, e
		}
		if h != want {
			return r, fmt.Errorf("post-activation identity mismatch: %s", filepath.Base(path))
		}
	}
	return r, nil
}
