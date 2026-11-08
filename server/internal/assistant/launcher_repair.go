package assistant

import (
	"context"
	"fmt"
	"path"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

// Repairing launchers (docs/server/launcher-health.md "Auto repair"): one round at a time per machine, since its launchers
// usually broke together. A CLI too old is the node's to update (launchers.repair); the rest go to an agent told to
// use manager.json's repair skill, on a quiet todo so nothing is pushed. What a round does not fix is blocked.

const launcherRepairUpgrade = "upgrade_cli"

// repairMachine starts a repair round for d's launchers that failed and are due one.
func (s *Service) repairMachine(ctx context.Context, d Device, cfg healthConfig) {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	if b.repairs[d.ID] != nil {
		b.mu.Unlock()
		return
	}
	var due []LauncherHealth
	for _, r := range b.recs {
		if r.Machine == d.ID && r.State == healthFailing && r.repairNow() {
			due = append(due, *r)
		}
	}
	b.mu.Unlock()
	var todo []LauncherHealth
	for _, r := range due {
		if r.Repairs >= healthMaxRepairs {
			s.setHealthState(ctx, d.ID, r.Launcher, healthBlocked, fmt.Sprintf("still failing after %d repairs: %s", r.Repairs, r.Error), nil)
			continue
		}
		todo = append(todo, r)
	}
	if len(todo) == 0 {
		return
	}

	// 1. A CLI too old: the node updates it (once for the machine), then those launchers are checked again.
	upgradeNote := ""
	var outdated []string
	for _, r := range todo {
		if r.Kind == errKindCLIOutdated && (r.Agent == "" || r.Agent == "claude") && r.Repairs == 0 {
			outdated = append(outdated, r.Launcher)
		}
	}
	if len(outdated) > 0 {
		var res proto.LauncherRepair
		rctx, cancel := context.WithTimeout(ctx, 6*time.Minute)
		err := s.call(rctx, d.ID, assistantwire.DeviceLaunchersRepair, map[string]any{"name": outdated[0], "action": launcherRepairUpgrade}, &res)
		cancel()
		fixed := LauncherFixed{Action: launcherRepairUpgrade, At: time.Now().UTC(), OK: err == nil && res.OK, Output: res.Output}
		if err != nil {
			fixed.Output = clip(oneLine(err.Error()), 400) // an older node has no launchers.repair
		}
		s.logf("launcher health: %s: updated the CLI: ok=%v %s", d.ID, fixed.OK, clip(fixed.Output, 160))
		for _, name := range outdated {
			s.setHealthState(ctx, d.ID, name, "", "", &fixed)
		}
		if fixed.OK {
			for _, name := range outdated {
				if res, ok := s.probeLauncherHealth(ctx, d.ID, name); ok {
					if s.recordHealth(ctx, d.ID, name, res).State == healthOK {
						s.repairWorked(ctx, d.ID, name, fixed)
					}
				}
			}
		} else {
			upgradeNote = "updating the CLI failed: " + fixed.Output + "; "
		}
	}
	var left []LauncherHealth
	for _, r := range todo {
		if now, ok := s.launcherHealth(ctx, d.ID, r.Launcher); ok && now.State == healthFailing && now.repairNow() { // not a usage limit met since
			left = append(left, now)
		}
	}
	if len(left) == 0 {
		return
	}

	// 2. The repair agent, when manager.json names its skill.
	if strings.TrimSpace(cfg.repair.Skill) == "" {
		for _, r := range left {
			s.setHealthState(ctx, d.ID, r.Launcher, healthBlocked, upgradeNote+"no repair agent is configured (manager.json agents.launcher_health.repair.skill)", nil)
		}
		return
	}
	if err := s.startRepairAgent(ctx, d, left, cfg); err != nil {
		s.logf("launcher health: %s: repair agent: %v", d.ID, err)
		for _, r := range left {
			s.setHealthState(ctx, d.ID, r.Launcher, healthBlocked, upgradeNote+"the repair agent did not start: "+err.Error(), nil)
		}
	}
}

// startRepairAgent starts the agent that repairs broken's launchers left, and marks them repairing.
func (s *Service) startRepairAgent(ctx context.Context, broken Device, left []LauncherHealth, cfg healthConfig) error {
	where := cfg.repair.Machine
	if where == "" {
		where = s.settings(ctx).MachineID
	}
	am, ok := s.machineByName(ctx, where)
	if !ok || !am.Reachable {
		return fmt.Errorf("the machine to repair from (%s) is not reachable", where)
	}
	launcher := s.startLauncher(ctx, cfg.repair.Launcher)
	if _, bad := s.launcherUnusable(ctx, am.ID, launcher); bad {
		launcher = ""
		for _, r := range s.healthRecords(ctx, healthOK, am.ID) {
			if r.Agent == "" || r.Agent == "claude" {
				launcher = r.Launcher
				break
			}
		}
		if launcher == "" {
			return fmt.Errorf("no launcher works on %s to run it", am.ID)
		}
	}
	cwd := cfg.repair.Cwd
	switch {
	case cwd == "" || cwd == "~":
		cwd = am.Home
	case strings.HasPrefix(cwd, "~/") && am.Home != "":
		cwd = path.Join(am.Home, cwd[2:])
	}
	if cwd == "" {
		return fmt.Errorf("%s has not reported its home directory yet", am.ID)
	}
	in := prompts.LauncherRepair{Skill: cfg.repair.Skill, Machine: broken.ID, SSHHost: broken.SSHHost}
	if broken.Name != broken.ID {
		in.Name = broken.Name
	}
	names := make([]string, 0, len(left))
	for _, r := range left {
		agent := r.Agent
		if agent == "" {
			agent = "claude"
		}
		in.Launchers = append(in.Launchers, prompts.LauncherRepairItem{Name: r.Launcher, Agent: agent, Kind: r.Kind, Error: r.Error})
		names = append(names, r.Launcher)
	}
	prompt := prompts.RenderLauncherRepair(in)
	params := map[string]any{"cwd": cwd, "launcher": launcher, "prompt": prompt, "tab_name": "launcher-repair"}
	params["permission_mode"] = cfg.repair.Permission // healthConfig's default when unset
	sctx, cancel := context.WithTimeout(ctx, taskStartTimeout)
	defer cancel()
	if err := s.applyModel(sctx, "agent.new", params); err != nil {
		return err
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	if err := s.call(sctx, am.ID, DeviceSessionStart, params, &res); err != nil {
		s.recordStartFailure(ctx, am.ID, launcher, err)
		return err
	}
	s.recordStartSuccess(ctx, am.ID, launcher)
	sid := res.Session.SessionID
	// A quiet todo with no conversation: the hub pushes nothing about its agent, and nothing tells a conversation.
	title := fmt.Sprintf("修复 %s 上的启动器", broken.Name)
	todo, err := s.newTodo(ctx, title, prompt, "", "", true)
	if err == nil {
		err = s.linkTodoAgent(ctx, todo.ID, am.ID, sid, title, cwd, "")
	}
	if err != nil {
		s.logf("launcher health: %s: repair todo: %v", broken.ID, err)
	}
	fixed := LauncherFixed{Action: "agent", At: time.Now().UTC(), Machine: am.ID, SessionID: sid, TodoID: todo.ID}
	for _, name := range names {
		s.setHealthState(ctx, broken.ID, name, healthRepairing, "", &fixed)
	}
	b := &s.health
	b.mu.Lock()
	b.repairs[broken.ID] = &machineRepair{Machine: broken.ID, Launchers: names, AgentMachine: am.ID, SessionID: sid, TodoID: todo.ID, StartedAt: fixed.At}
	s.healthSaveLocked(ctx)
	b.mu.Unlock()
	s.logf("launcher health: %s: repair agent %s on %s for %s", broken.ID, short(sid), am.ID, strings.Join(names, ", "))
	return nil
}

// followRepairs checks again the launchers of each repair agent that stopped (or ran out of time): those that work
// now are fixed, the rest blocked.
func (s *Service) followRepairs(ctx context.Context, cfg healthConfig) {
	b := &s.health
	b.mu.Lock()
	s.healthLoadLocked(ctx)
	list := make([]machineRepair, 0, len(b.repairs))
	for _, r := range b.repairs {
		list = append(list, *r)
	}
	b.mu.Unlock()
	for _, rep := range list {
		age := time.Since(rep.StartedAt)
		if age < repairSettle {
			continue
		}
		c, found := s.session(ctx, rep.AgentMachine, rep.SessionID)
		stopped := !found || (c.State != proto.StateWorking && c.State != proto.StateNeedsYou && c.Status != proto.StatusWorking)
		if !stopped && age < repairDeadline {
			continue
		}
		said := ""
		if found && strings.TrimSpace(c.LastText) != "" {
			said = "; the repair agent said: " + clip(oneLine(c.LastText), 300)
		}
		d, ok := s.device(ctx, rep.Machine)
		if !ok || !d.Reachable {
			if age < 2*repairDeadline {
				continue // check when it is back
			}
			for _, name := range rep.Launchers {
				s.setHealthState(ctx, rep.Machine, name, healthBlocked, "the machine was not reachable to check after the repair"+said, nil)
			}
		} else {
			for _, name := range rep.Launchers {
				res, ok := s.probeLauncherHealth(ctx, rep.Machine, name)
				if !ok {
					s.setHealthState(ctx, rep.Machine, name, healthBlocked, "could not check it after the repair"+said, nil)
					continue
				}
				rec := s.recordHealth(ctx, rep.Machine, name, res)
				if rec.State == healthOK {
					s.repairWorked(ctx, rep.Machine, name, LauncherFixed{Action: "agent", At: rep.StartedAt, Machine: rep.AgentMachine,
						SessionID: rep.SessionID, TodoID: rep.TodoID, Output: strings.TrimPrefix(said, "; the repair agent said: ")})
					continue
				}
				s.setHealthState(ctx, rep.Machine, name, healthBlocked, "still failing after the repair agent: "+rec.Error+said, nil)
			}
		}
		b.mu.Lock()
		delete(b.repairs, rep.Machine)
		s.healthSaveLocked(ctx)
		b.mu.Unlock()
	}
}
