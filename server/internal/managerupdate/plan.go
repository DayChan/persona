// Package managerupdate controls an explicitly authorized update of one independent manager.
package managerupdate

import (
	"crypto/sha256"
	"debug/buildinfo"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"time"

	"github.com/nwverse/persona/server/internal/update"
)

// Plan is produced by the existing release publisher, never inferred from a default manager.
// Idle evidence covers Hub sessions only. BootstrapWindow is explicit maintenance authorization
// for older managers that cannot report background idleness; it is not an idle assertion.
type Plan struct {
	Schema              int       `json:"schema"`
	RunID               string    `json:"run_id"`
	ManagerID           string    `json:"manager_id"`
	Dir                 string    `json:"dir"`
	Binary              string    `json:"binary"`
	Unit                string    `json:"unit"`
	PlistPath           string    `json:"plist_path"`
	UID                 int       `json:"uid"`
	PreviousSHA256      string    `json:"previous_sha256"`
	ConfigSHA256        string    `json:"config_sha256"`
	PlistSHA256         string    `json:"plist_sha256"`
	Source              string    `json:"source"`
	Version             string    `json:"version"`
	BinarySHA256        string    `json:"binary_sha256"`
	Size                int64     `json:"size"`
	ActivationEnabled   bool      `json:"activation_enabled"`
	BootstrapAuthorized bool      `json:"bootstrap_authorized"`
	BootstrapWindow     time.Time `json:"bootstrap_window_until"`
	IdleVerifiedAt      time.Time `json:"idle_verified_at"`
	IdleValidUntil      time.Time `json:"idle_valid_until"`
	IdleSnapshotSHA256  string    `json:"idle_snapshot_sha256"`
	Freeze              bool      `json:"freeze"`
}

var idPattern = regexp.MustCompile(`^[a-z][a-z0-9-]{0,31}$`)
var runPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,80}$`)
var shaPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var sourcePattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

func ReadPlan(path string) (Plan, error) {
	var p Plan
	f, err := os.Open(path)
	if err != nil {
		return p, err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 64*1024))
	d.DisallowUnknownFields()
	if err = d.Decode(&p); err != nil {
		return p, err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return p, errors.New("plan must contain exactly one JSON object")
	}
	return p, nil
}

func hashFile(path string) (string, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", e
	}
	defer f.Close()
	h := sha256.New()
	if _, e = io.Copy(h, f); e != nil {
		return "", e
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func canonical(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("noncanonical absolute path: %q", path)
	}
	resolved, e := filepath.EvalSymlinks(path)
	if e != nil {
		return e
	}
	if resolved != path {
		return fmt.Errorf("symlink path rejected: %s", path)
	}
	return nil
}

func (p Plan) validateShape() error {
	if p.Schema != 1 || !runPattern.MatchString(p.RunID) || !idPattern.MatchString(p.ManagerID) || p.ManagerID == "assistant" {
		return errors.New("invalid schema, run ID or independent manager ID")
	}
	if !sourcePattern.MatchString(p.Source) || p.Version == "" || p.Size <= 0 || p.UID < 0 {
		return errors.New("invalid release identity")
	}
	for _, s := range []string{p.PreviousSHA256, p.ConfigSHA256, p.PlistSHA256, p.BinarySHA256, p.IdleSnapshotSHA256} {
		if !shaPattern.MatchString(s) {
			return errors.New("invalid SHA256")
		}
	}
	if p.Binary != filepath.Join(p.Dir, "bin", "personad") {
		return errors.New("target must be the independent manager directory's bin/personad")
	}
	if p.Unit == "" || filepath.Base(p.Unit) != p.Unit || filepath.Base(p.PlistPath) != p.Unit+".plist" {
		return errors.New("invalid original launchd unit")
	}
	return nil
}

func (p Plan) authorize(now time.Time) error {
	if p.Freeze || !p.ActivationEnabled || !p.BootstrapAuthorized {
		return errors.New("activation disabled, frozen or bootstrap not authorized")
	}
	if !p.BootstrapWindow.After(now) || p.BootstrapWindow.Sub(now) > 15*time.Minute {
		return errors.New("explicit bootstrap maintenance window missing or too long")
	}
	if p.IdleVerifiedAt.After(now) || !p.IdleValidUntil.After(now) || p.IdleValidUntil.Sub(p.IdleVerifiedAt) > 60*time.Second || now.Sub(p.IdleVerifiedAt) > 60*time.Second {
		return errors.New("fresh scoped session idle evidence required (at most 60 seconds)")
	}
	return nil
}

func validateRelease(p Plan, build update.Build, exe string) error {
	if build.Channel != update.Release || build.Number <= 0 || build.Commit != p.Source || build.Version != p.Version {
		return errors.New("executor must be the exact same-source formal release")
	}
	info, e := buildinfo.ReadFile(exe)
	if e != nil {
		return e
	}
	if info.Path != "github.com/nwverse/persona/server/cmd/personad" {
		return errors.New("executor is not the full personad program")
	}
	revision, modified := "", ""
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.modified":
			modified = s.Value
		}
	}
	if revision != p.Source || modified != "false" {
		return errors.New("executor has no matching clean full source identity")
	}
	st, e := os.Stat(exe)
	if e != nil {
		return e
	}
	h, e := hashFile(exe)
	if e != nil {
		return e
	}
	if st.Size() != p.Size || h != p.BinarySHA256 {
		return errors.New("release size or native SHA256 mismatch")
	}
	return nil
}
