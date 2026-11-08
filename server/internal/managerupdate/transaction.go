package managerupdate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"path/filepath"
	"time"
)

type Receipt struct {
	Schema    int       `json:"schema"`
	RunID     string    `json:"run_id"`
	ManagerID string    `json:"manager_id"`
	Phase     string    `json:"phase"`
	Error     string    `json:"error,omitempty"`
	UpdatedAt time.Time `json:"updated_at"`
	PID       int       `json:"pid,omitempty"`
	Ready     *Ready    `json:"ready,omitempty"`
}

func receipt(p Plan, phase string, e error) Receipt {
	r := Receipt{Schema: 1, RunID: p.RunID, ManagerID: p.ManagerID, Phase: phase, UpdatedAt: time.Now().UTC()}
	if e != nil {
		r.Error = e.Error()
	}
	return r
}
func txDir(p Plan) string { return filepath.Join(p.Dir, ".manager-updates", p.RunID, "transaction") }
func readReceipt(p Plan) (Receipt, error) {
	var r Receipt
	if e := p.validateShape(); e != nil {
		return r, e
	}
	path := filepath.Join(txDir(p), "receipt.json")
	if e := canonical(path); e != nil {
		return r, e
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return r, e
	}
	e = json.Unmarshal(b, &r)
	if e == nil && (r.Schema != 1 || r.RunID != p.RunID || r.ManagerID != p.ManagerID) {
		e = errors.New("receipt transaction identity mismatch")
	}
	return r, e
}
func writeJSON(path string, v any) error {
	b, e := json.Marshal(v)
	if e != nil {
		return e
	}
	return atomicWrite(path, b, 0600)
}
func atomicWrite(path string, b []byte, mode os.FileMode) error {
	f, e := os.CreateTemp(filepath.Dir(path), ".manager-update-*")
	if e != nil {
		return e
	}
	tmp := f.Name()
	if e = f.Chmod(mode); e != nil {
		return errors.Join(e, f.Close(), os.Remove(tmp))
	}
	_, we := f.Write(b)
	e = errors.Join(we, f.Sync(), f.Close())
	if e != nil {
		return errors.Join(e, os.Remove(tmp))
	}
	if e = os.Rename(tmp, path); e != nil {
		return errors.Join(e, os.Remove(tmp))
	}
	d, e := os.Open(filepath.Dir(path))
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}
func copyExclusive(from, to string) error {
	f, e := os.Open(from)
	if e != nil {
		return e
	}
	defer f.Close()
	g, e := os.OpenFile(to, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if e != nil {
		return e
	}
	_, we := io.Copy(g, f)
	if e = errors.Join(we, g.Sync(), g.Close()); e != nil {
		return e
	}
	d, e := os.Open(filepath.Dir(to))
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}

// Start adds a separate watchdog job; the original manager's plist and environment stay byte-for-byte intact.
func (c *Controller) Start(ctx context.Context, p Plan) error {
	if e := p.authorize(c.Now()); e != nil {
		return e
	}
	if e := c.Check(ctx, p); e != nil {
		return e
	}
	unlock, e := acquire(filepath.Join(p.Dir, ".manager-update.lock"))
	if e != nil {
		return e
	}
	defer unlock()
	if e = p.authorize(c.Now()); e != nil {
		return e
	}
	if e = rejectPendingActivation(p); e != nil {
		return e
	}
	dir := txDir(p)
	for _, parent := range []string{filepath.Join(p.Dir, ".manager-updates"), filepath.Dir(dir)} {
		if e = os.Mkdir(parent, 0700); e != nil && !os.IsExist(e) {
			return e
		}
		if e = canonical(parent); e != nil {
			return e
		}
	}
	if e = os.Mkdir(dir, 0700); e != nil {
		return fmt.Errorf("transaction already exists or cannot be reserved: %w", e)
	}
	executor := filepath.Join(dir, "executor")
	if e = copyExclusive(c.Exe, executor); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(dir, "plan.json"), p); e != nil {
		return e
	}
	if e = writeJSON(filepath.Join(dir, "receipt.json"), receipt(p, "checked", nil)); e != nil {
		return e
	}
	label := "com.persona.manager-update." + p.RunID
	x := html.EscapeString
	plist := `<?xml version="1.0" encoding="UTF-8"?><plist version="1.0"><dict><key>Label</key><string>` + label + `</string><key>ProgramArguments</key><array><string>` + x(executor) + `</string><string>manager</string><string>update</string><string>run</string><string>--plan</string><string>` + x(filepath.Join(dir, "plan.json")) + `</string></array><key>RunAtLoad</key><true/><key>KeepAlive</key><dict><key>SuccessfulExit</key><false/></dict><key>ThrottleInterval</key><integer>5</integer><key>StandardOutPath</key><string>` + x(filepath.Join(dir, "watchdog.log")) + `</string><key>StandardErrorPath</key><string>` + x(filepath.Join(dir, "watchdog.log")) + `</string></dict></plist>`
	path := filepath.Join(c.Home, "Library", "LaunchAgents", label+".plist")
	if _, e = os.Lstat(path); !os.IsNotExist(e) {
		return errors.New("watchdog unit already exists or cannot be inspected")
	}
	if e = atomicWrite(path, []byte(plist), 0600); e != nil {
		return e
	}
	_, e = c.Command(ctx, "/bin/launchctl", "bootstrap", fmt.Sprintf("gui/%d", p.UID), path)
	return e
}

// Run is restartable by launchd. A pending journal always restores the old image;
// it never retries an interrupted candidate activation.
func (c *Controller) Run(ctx context.Context, p Plan) (Receipt, error) {
	r := receipt(p, "checked", nil)
	if e := p.validateShape(); e != nil {
		return r, e
	}
	if c.GOOS != "darwin" || c.UID != p.UID {
		return r, errors.New("wrong operating system or owner")
	}
	if e := canonical(txDir(p)); e != nil {
		return r, e
	}
	initial, e := readReceipt(p)
	if e != nil {
		return r, e
	}
	if initial.Phase == "checked" && initial.Error != "" {
		return initial, nil
	}
	var pending bool

	unlock, e := acquire(filepath.Join(p.Dir, ".manager-update.lock"))
	if e != nil {
		return r, e
	}
	defer unlock()
	saved, e := readReceipt(p)
	if e != nil {
		return r, e
	}
	if saved.Phase == "confirmed" || saved.Phase == "rollback" {
		return saved, nil
	}
	pending, e = activePending(p)
	if e != nil {
		return r, e
	}
	if saved.Phase == "applied" || (saved.Phase == "checked" && pending) {
		if !pending {
			return r, errors.New("interrupted activation no longer owns the active transaction")
		}
		return c.rollback(ctx, p, errors.New("interrupted activation recovered; candidate will not be retried"))
	}
	if saved.Phase != "checked" {
		return r, errors.New("unknown transaction phase")
	}
	if e = p.authorize(c.Now()); e != nil {
		return c.stop(p, e)
	}
	if e = c.Check(ctx, p); e != nil {
		return c.stop(p, e)
	}
	// The second authorization follows all slow inspection and immediately precedes binary writes.
	if e = p.authorize(c.Now()); e != nil {
		return c.stop(p, e)
	}
	prev := filepath.Join(txDir(p), "previous")
	if e = copyExclusive(p.Binary, prev); e != nil {
		return c.stop(p, e)
	}
	// Mark pending before any target replacement, including a crash during atomic rename.
	if e = writeJSON(activePath(p), p); e != nil {
		return r, e
	}
	r = receipt(p, "applied", nil)
	if e = writeJSON(filepath.Join(txDir(p), "receipt.json"), r); e != nil {
		return r, e
	}
	if e = replaceImage(c.Exe, p.Binary); e != nil {
		return c.rollback(ctx, p, e)
	}
	if _, e = c.Command(ctx, "/bin/launchctl", "kickstart", "-k", c.service(p)); e != nil {
		return c.rollback(ctx, p, e)
	}
	healthCtx, cancelHealth := context.WithTimeout(ctx, 90*time.Second)
	defer cancelHealth()
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return c.rollback(context.WithoutCancel(ctx), p, ctx.Err())
		case <-timer.C:
			return c.rollback(context.WithoutCancel(ctx), p, errors.New("new manager readiness timed out"))
		case <-tick.C:
			decision, e := readDecision(p)
			if e != nil {
				return c.rollback(ctx, p, e)
			}
			if decision == "rollback" {
				return c.rollback(ctx, p, errors.New("publisher rejected post-activation Hub identity checks"))
			}
			ready, e := c.liveReady(healthCtx, p)
			if e != nil {
				continue
			}
			r = receipt(p, "applied", nil)
			r.Ready = &ready
			r.PID = ready.PID
			if decision == "confirm" {
				r.Phase = "confirmed"
			}
			if e = writeJSON(filepath.Join(txDir(p), "receipt.json"), r); e != nil {
				return c.rollback(ctx, p, e)
			}
			if r.Phase == "confirmed" {
				if e := clearActive(p); e != nil {
					log.Printf("manager update terminal marker cleanup: %v", e)
				}
				return r, nil
			}
		}
	}
}
func replaceImage(from, to string) error {
	b, e := os.ReadFile(from)
	if e != nil {
		return e
	}
	st, e := os.Stat(to)
	if e != nil {
		return e
	}
	return atomicWrite(to, b, st.Mode().Perm())
}
func (c *Controller) rollback(ctx context.Context, p Plan, cause error) (Receipt, error) {
	r := receipt(p, "rollback", cause)
	if e := requireActive(p); e != nil {
		return r, e
	}
	for _, path := range []string{p.Dir, p.Binary, p.PlistPath, filepath.Join(p.Dir, "manager.json")} {
		if e := canonical(path); e != nil {
			return r, e
		}
	}
	if e := c.ReleaseCheck(p, c.Build, c.Exe); e != nil {
		return r, e
	}
	if e := c.checkUnit(ctx, p, false); e != nil {
		return r, e
	}
	for path, want := range map[string]string{p.PlistPath: p.PlistSHA256, filepath.Join(p.Dir, "manager.json"): p.ConfigSHA256} {
		h, e := hashFile(path)
		if e != nil || h != want {
			return r, errors.Join(errors.New("rollback scope identity changed"), e)
		}
	}
	current, e := hashFile(p.Binary)
	if e != nil {
		return r, e
	}
	if current != p.PreviousSHA256 && current != p.BinarySHA256 {
		return r, errors.New("rollback refused: unexpected external image replaced the target")
	}
	prev := filepath.Join(txDir(p), "previous")
	if e := canonical(prev); e != nil {
		return r, e
	}
	h, e := hashFile(prev)
	if e != nil || h != p.PreviousSHA256 {
		return r, errors.Join(cause, errors.New("rollback image missing or mismatched"), e)
	}
	if e = replaceImage(prev, p.Binary); e != nil {
		return r, errors.Join(cause, e)
	}
	if _, e = c.Command(ctx, "/bin/launchctl", "kickstart", "-k", c.service(p)); e != nil {
		return r, errors.Join(cause, e)
	}
	if e = writeJSON(filepath.Join(txDir(p), "receipt.json"), r); e != nil {
		return r, errors.Join(cause, e)
	}
	// Rollback is a terminal, unsuccessful update receipt, but the watchdog exits cleanly.
	if e := clearActive(p); e != nil {
		log.Printf("manager update terminal marker cleanup: %v", e)
	}
	return r, nil
}

func (c *Controller) stop(p Plan, cause error) (Receipt, error) {
	r := receipt(p, "checked", cause)
	if e := writeJSON(filepath.Join(txDir(p), "receipt.json"), r); e != nil {
		return r, errors.Join(cause, e)
	}
	return r, nil
}
func activePending(p Plan) (bool, error) {
	path := activePath(p)
	if _, e := os.Lstat(path); os.IsNotExist(e) {
		return false, nil
	} else if e != nil {
		return false, e
	}
	if e := canonical(path); e != nil {
		return false, e
	}
	a, e := ReadPlan(path)
	if e != nil {
		return false, e
	}
	if a.RunID != p.RunID {
		return false, errors.New("another transaction owns the active manager update")
	}
	if !sameTarget(a, p) || a.Source != p.Source || a.Version != p.Version || a.Size != p.Size || a.BinarySHA256 != p.BinarySHA256 || a.PreviousSHA256 != p.PreviousSHA256 {
		return false, errors.New("active transaction identity mismatch")
	}
	return true, nil
}

func clearActive(p Plan) error {
	pending, e := activePending(p)
	if e != nil {
		return e
	}
	if !pending {
		return nil
	}
	if e = os.Remove(activePath(p)); e != nil && !os.IsNotExist(e) {
		return e
	}
	d, e := os.Open(p.Dir)
	if e != nil {
		return e
	}
	return errors.Join(d.Sync(), d.Close())
}
