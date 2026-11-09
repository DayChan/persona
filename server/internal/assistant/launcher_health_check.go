package assistant

import (
	"context"
	"fmt"
	"path"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/compat"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- checking ----

// probeLauncherHealth runs launchers.check for launcher on machine; false when the machine could not be asked (no result).
func (s *Service) probeLauncherHealth(ctx context.Context, machine, launcher string) (healthResult, bool) {
	ctx, cancel := context.WithTimeout(ctx, healthCheckTimeout)
	defer cancel()
	var res proto.LauncherCheck
	if err := s.call(ctx, machine, assistantwire.DeviceLaunchersCheck, map[string]any{"name": launcher}, &res); err != nil {
		s.logf("launcher health: check %s on %s: %v", launcher, machine, err)
		return healthResult{}, false
	}
	return healthResult{ok: res.OK, agent: res.Agent, model: res.Model, millis: res.Millis, err: res.Error, source: healthSourceCheck}, true
}

// healthLaunchers are the launchers to check on machine: those it can start (its ~/.zshrc's, the managed ones) and
// the default one; not a runtime's (they are not run with a prompt) nor an API provider's.
func (s *Service) healthLaunchers(ctx context.Context, machine string) ([]string, error) {
	list, err := s.machineLaunchers(ctx, machine)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, l := range list {
		if l.Source == "" || machineSource(l.Source) || compat.Source(l.Source) == "persona" {
			out = append(out, l.Name)
		}
	}
	if d := s.startLauncher(ctx, ""); !slices.Contains(out, d) {
		out = append(out, d)
	}
	return out, nil
}

// startLauncher is the launcher a start with launcher runs: itself, else model settings › Agents', else claude.
func (s *Service) startLauncher(ctx context.Context, launcher string) string {
	if launcher != "" {
		return launcher
	}
	if b, err := s.resolveModel(ctx, "agent.new"); err == nil {
		if name, ok := strings.CutPrefix(b.Provider, "launcher."); ok && name != "" {
			return name
		}
	}
	return "claude"
}

// healthRound is one look at the machines: the launchers due on each reachable one are checked (each machine's one
// after another, the machines side by side), then repaired; the repair agents at work are followed. wait: return
// when it is all done (tests, a forced check), else it goes on in the background.
func (s *Service) healthRound(ctx context.Context, wait bool) {
	cfg := s.healthConfig(ctx)
	devices := s.devices(ctx)
	b := &s.health
	var wg sync.WaitGroup
	for _, d := range devices {
		b.mu.Lock()
		s.healthLoadLocked(ctx)
		cameOnline := d.Reachable && !b.online[d.ID]
		b.online[d.ID] = d.Reachable
		busy := b.checking[d.ID]
		if d.Reachable && cfg.on && !busy {
			b.checking[d.ID] = true
		}
		b.mu.Unlock()
		if !d.Reachable || !cfg.on || busy {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer func() {
				b.mu.Lock()
				delete(b.checking, d.ID)
				b.mu.Unlock()
			}()
			s.checkMachine(ctx, d, cfg, cameOnline, "")
			s.repairMachine(ctx, d, cfg)
		}()
	}
	if wait {
		wg.Wait()
	}
	s.followRepairs(ctx, cfg)
}

// checkMachine checks machine d's launchers that are due (only: one by that name, at once); the records of launchers
// it no longer has go.
func (s *Service) checkMachine(ctx context.Context, d Device, cfg healthConfig, cameOnline bool, only string) []LauncherHealth {
	names, err := s.healthLaunchers(ctx, d.ID)
	if err != nil {
		s.logf("launcher health: %s: launchers: %v", d.ID, err)
		return nil
	}
	b := &s.health
	b.mu.Lock()
	for key, r := range b.recs {
		if r.Machine == d.ID && !slices.Contains(names, r.Launcher) {
			delete(b.recs, key)
		}
	}
	b.mu.Unlock()
	var out []LauncherHealth
	for _, name := range names {
		if ctx.Err() != nil {
			break
		}
		if only != "" && name != only {
			continue
		}
		if only == "" {
			r, ok := s.launcherHealth(ctx, d.ID, name)
			since := time.Since(r.CheckedAt)
			if reset, limited := r.limitReset(); limited {
				// A check before the reset is refused too, and spends a request for it; the first round after it
				// checks, whatever the interval. From a minute after, as the manager continues a turn its limit
				// stopped (limitContinueAfter): one right at the reset may still be refused.
				if time.Now().Before(reset.Add(limitContinueAfter)) {
					continue
				}
			} else if ok && since < cfg.interval && !(cameOnline && since >= healthOnlineRecheck) {
				continue
			}
			if ok && r.Kind == errKindNotInTerminal && r.unusable() {
				continue // the login shell's check cannot tell (recordHealth): it waits for a start, or goes stale
			}
		}
		if res, ok := s.probeLauncherHealth(ctx, d.ID, name); ok {
			out = append(out, s.recordHealth(ctx, d.ID, name, res))
		}
	}
	return out
}

// ---- picking a machine ----

// healthyMachine is another reachable machine that runs launcher and whose check of it did not fail: one where it
// passed first, then one where it was never checked.
func (s *Service) healthyMachine(ctx context.Context, launcher, except string) (Device, bool) {
	best, rank := Device{}, 3
	for _, d := range s.devices(ctx) {
		if d.ID == except || !d.Reachable {
			continue
		}
		r, ok := s.launcherHealth(ctx, d.ID, launcher)
		n := 1
		switch {
		case ok && r.unusable():
			continue
		case ok && r.State == healthOK:
			n = 0
		}
		if n >= rank || !s.launcherOn(ctx, d.ID, launcher) {
			continue
		}
		best, rank = d, n
	}
	return best, rank < 3
}

// launcherOn: machine has launcher (a managed one is on every machine; a failed list says no).
func (s *Service) launcherOn(ctx context.Context, machine, launcher string) bool {
	if launcher == "claude" || slices.ContainsFunc(s.storedLaunchers(ctx), func(l storedLauncher) bool { return l.Name == launcher && l.Kind != "api" }) {
		return true
	}
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	list, err := s.machineLaunchers(cctx, machine)
	return err == nil && slices.ContainsFunc(list, func(l proto.Launcher) bool { return l.Name == launcher })
}

// rehome moves dir from one machine's home to another's (a path outside the home stays).
func rehome(dir string, from, to Device) string {
	if from.Home == "" || to.Home == "" || from.Home == to.Home {
		return dir
	}
	if dir == from.Home {
		return to.Home
	}
	if rest, ok := strings.CutPrefix(dir, from.Home+"/"); ok {
		return path.Join(to.Home, rest)
	}
	return dir
}

// healthDetour is where a start with launcher on machine m goes instead: another machine when its last check failed
// there and another has it healthy (moved), with a line saying so for whoever started it; no other: m, and the line
// says it may fail. A usage limit with a known reset says when that is.
func (s *Service) healthDetour(ctx context.Context, m Device, launcher string) (Device, string, bool) {
	r, bad := s.launcherUnusable(ctx, m.ID, launcher)
	if !bad {
		return m, "", false
	}
	alt, moved := s.healthyMachine(ctx, launcher, m.ID)
	if note := r.resetNote(); note != "" {
		what := fmt.Sprintf("launcher %s is at its subscription usage limit on %s (%s)", launcher, m.ID, note)
		if moved {
			return alt, fmt.Sprintf("%s, so this runs on %s instead", what, alt.ID), true
		}
		return m, what + " and no other machine has it healthy; started there anyway", false
	}
	why := fmt.Sprintf("%s: %s", r.Kind, clip(r.Error, 160))
	if moved {
		return alt, fmt.Sprintf("launcher %s failed its health check on %s (%s), so this runs on %s instead", launcher, m.ID, why, alt.ID), true
	}
	return m, fmt.Sprintf("launcher %s failed its last health check on %s (%s) and no other machine has it healthy; started there anyway", launcher, m.ID, why), false
}

// healthLines are agents_list's lines about the launchers that do not work (machine: only there).
func (s *Service) healthLines(ctx context.Context, machine string) string {
	var b strings.Builder
	for _, r := range s.healthRecords(ctx, "", machine) {
		if !r.unusable() {
			continue
		}
		if b.Len() == 0 {
			b.WriteString("Launchers that do not work now (agent_start / task_plan move off them):\n")
		}
		fmt.Fprintf(&b, "- machine=%s launcher=%s · %s · %s: %s · checked %s", r.Machine, r.Launcher, r.State, r.Kind, clip(r.Error, 200), ago(r.CheckedAt))
		if note := r.resetNote(); note != "" {
			b.WriteString(" · " + note)
		}
		b.WriteString("\n")
	}
	return b.String()
}

// launcherWithHealth is a launcher of the "New Agent" picker with how its last check went on that machine.
type launcherWithHealth struct {
	ConfiguredLauncher
	Health *launcherHealthView `json:"health,omitempty"`
}

type launcherHealthView struct {
	State     string     `json:"state"`
	Kind      string     `json:"kind,omitempty"`
	Error     string     `json:"error,omitempty"`
	CheckedAt time.Time  `json:"checked_at"`
	ResetsAt  *time.Time `json:"resets_at,omitempty"` // a usage limit's reset: unusable until then
}

// withHealth puts machine's health records on its launchers (none: never checked).
func (s *Service) withHealth(ctx context.Context, machine string, list []ConfiguredLauncher) []launcherWithHealth {
	out := make([]launcherWithHealth, 0, len(list))
	for _, l := range list {
		v := launcherWithHealth{ConfiguredLauncher: l}
		if r, ok := s.launcherHealth(ctx, machine, l.Name); ok && (r.State == healthOK || r.unusable()) { // not a failure too old to count
			v.Health = &launcherHealthView{State: r.State, Kind: r.Kind, Error: r.Error, CheckedAt: r.CheckedAt}
			if reset, limited := r.limitReset(); limited {
				v.Health.ResetsAt = &reset
			}
		}
		out = append(out, v)
	}
	return out
}
