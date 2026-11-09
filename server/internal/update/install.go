package update

import (
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/identity"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"time"
)

// Swap puts newBin in target's place: target is first copied to target.prev (the version to go back to), then newBin
// is copied next to it and renamed over it, so target is never half-written. It returns the .prev path, also when
// only putting newBin in place failed (target is then as it was), and "" when target.prev could not be written.
func Swap(target, newBin string) (string, error) {
	prev := target + ".prev"
	if err := copyFile(target, prev); err != nil {
		return "", err
	}
	if err := copyFile(newBin, target); err != nil {
		return prev, err
	}
	return prev, nil
}

// Restore puts prev back in target's place (prev stays).
func Restore(target, prev string) error { return copyFile(prev, target) }

// copyFile copies from to to through to.new in the same directory and a rename (replaceFile).
func copyFile(from, to string) error {
	in, err := os.Open(from)
	if err != nil {
		return err
	}
	defer in.Close()
	return replaceFile(to, 0o755, func(out *os.File) error {
		_, err := io.Copy(out, in)
		return err
	})
}

// replaceFile puts what write writes in path's place through path.new in the same directory and a rename. The temp
// file is synced before the rename and the directory after it, so a crash or power loss right after (the hub execs the
// new binary next) leaves the old file or the whole new one, never an empty or partial one. A failure removes the
// temp file.
func replaceFile(path string, perm os.FileMode, write func(*os.File) error) error {
	tmp := path + ".new"
	out, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, perm)
	if err != nil {
		return err
	}
	err = write(out)
	if err == nil {
		err = out.Sync()
	}
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path))
	return nil
}

// syncDir makes a rename in dir durable, best effort. Windows cannot sync a directory handle, so it is skipped there.
func syncDir(dir string) {
	if runtime.GOOS == "windows" {
		return
	}
	if d, err := os.Open(dir); err == nil {
		_ = d.Sync()
		d.Close()
	}
}

// Pending is an update under way, kept next to the binary across the restart (docs/operations/hub-update.md "Preparing"): the one
// piece of hub state outside Postgres, because going back must work when the new version cannot reach the database
// or does not start at all.
type Pending struct {
	FromVersion string    `json:"from_version"`
	FromBuild   int64     `json:"from_build"`
	ToVersion   string    `json:"to_version"`
	ToBuild     int64     `json:"to_build"`
	Target      string    `json:"target"` // the binary the hub runs
	Prev        string    `json:"prev"`   // the one to go back to
	Started     time.Time `json:"started"`
	Online      []string  `json:"online"` // the machines connected before the restart: they must come back
	Starts      int       `json:"starts"` // how many times the new version started (crash loops)
	Auto        bool      `json:"auto"`
	PID         int       `json:"pid"`        // the hub's process
	Args        []string  `json:"args"`       // how it was started
	HealthURL   string    `json:"health_url"` // where the watchdog asks
	Nodes       string    `json:"nodes"`      // the downloaded node binaries, put in place once confirmed
	// Programs are the programs swapped with the hub (personad-wechat): going back puts them back too.
	Programs []Swapped `json:"programs,omitempty"`
	// Run is the program the hub goes on as, when it is not Target: the persona-hub swapped with personad, for a hub
	// that runs as persona-hub. Target stays personad, the release's main download, so a build that does not know Run
	// (an older edge or watchdog) runs personad, which serves the same hub (RunPath).
	Run string `json:"run,omitempty"`
}

// RunPath is the binary the hub runs after the update and again after going back: Run, or Target for a pending
// without one (a hub that runs as personad, or a pending an older build wrote).
func (p Pending) RunPath() string {
	if p.Run != "" {
		return p.Run
	}
	return p.Target
}

// Swapped is one program swapped with the hub: Prev is the one to go back to, "" when there was none (going back
// removes it). The zero Swapped is a swap that changed nothing.
type Swapped struct {
	Target string `json:"target"`
	Prev   string `json:"prev,omitempty"`
}

// SwapProgram puts a program's new build in place like Swap, also when the hub had none of it before. When it fails,
// target is as it was and the Swapped it returns is still safe to go back with.
func SwapProgram(target, newBin string) (Swapped, error) {
	if _, err := os.Stat(target); errors.Is(err, os.ErrNotExist) {
		return Swapped{Target: target}, copyFile(newBin, target)
	}
	prev, err := Swap(target, newBin)
	if prev == "" {
		return Swapped{}, err
	}
	return Swapped{Target: target, Prev: prev}, err
}

// RestoreAll goes back to what ran before the update: the hub's binary and every program swapped with it (one that
// was new is removed). It goes on past a failure, returning the first.
func RestoreAll(p Pending) error {
	var first error
	keep := func(err error) {
		if first == nil {
			first = err
		}
	}
	if p.Prev != "" {
		if err := Restore(p.Target, p.Prev); err != nil {
			keep(err)
		}
	}
	for _, s := range p.Programs {
		var err error
		if s.Target == "" {
			continue
		}
		if s.Prev == "" {
			err = os.Remove(s.Target)
			if errors.Is(err, os.ErrNotExist) {
				err = nil
			}
		} else {
			err = Restore(s.Target, s.Prev)
		}
		if err != nil {
			keep(err)
		}
	}
	return first
}

// RolledBack says an update went back, for the old version to record when it starts (history, bad build).
type RolledBack struct {
	FromVersion string    `json:"from_version"`
	ToVersion   string    `json:"to_version"`
	ToBuild     int64     `json:"to_build"`
	Started     time.Time `json:"started"`
	At          time.Time `json:"at"`
	Reason      string    `json:"reason"`
	Auto        bool      `json:"auto"`
}

// Dir is where the markers and downloads live: ~/.persona/update.
func Dir() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(identity.DataDir(home), "update")
}

func PendingPath(dir string) string    { return filepath.Join(dir, "pending.json") }
func RolledBackPath(dir string) string { return filepath.Join(dir, "rolledback.json") }

// Load reads a marker; ok is false when there is none.
func Load(path string, v any) (bool, error) {
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, json.Unmarshal(b, v)
}

// Save writes a marker whole (a synced temp file and a rename, replaceFile).
func Save(path string, v any) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return replaceFile(path, 0o600, func(out *os.File) error {
		_, err := out.Write(b)
		return err
	})
}
