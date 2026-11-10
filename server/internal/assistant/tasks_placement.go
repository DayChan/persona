package assistant

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

const (
	machineFixed    = "fixed"
	machineFlexible = "flexible"
)

// Missing policies on older saved plans are fixed too: inferred intent cannot
// authorize moving work (and its local directory) to a different computer.
func (t *PlanTask) normalizeMachinePolicy() error {
	if t.MachinePolicy == "" {
		t.MachinePolicy = machineFixed
	}
	if t.MachinePolicy != machineFixed && t.MachinePolicy != machineFlexible {
		return fmt.Errorf("unknown machine_policy %q; use fixed or flexible", t.MachinePolicy)
	}
	return nil
}

// placeTask names t's machine by its id and makes ~ in its directory that machine's home; false: no such machine.
func (s *Service) placeTask(ctx context.Context, t *PlanTask) bool {
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	t.Machine = m.ID
	t.Cwd = proto.ExpandHome(t.Cwd, m.Home)
	return true
}

// healthyPlace moves t off a machine where its launcher failed its health check, to one where it works and the directory exists
// (launcher_health_check.go); its why says so. False: it stays.
func (s *Service) healthyPlace(ctx context.Context, t *PlanTask) bool {
	if t.MachinePolicy != machineFlexible {
		return false
	}
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return false
	}
	alt, note, moved := s.healthDetour(ctx, m, s.startLauncher(ctx, t.Launcher), func(alt Device) bool {
		return s.taskDirectoryExists(ctx, alt.ID, rehome(t.Cwd, m, alt))
	})
	t.noteWhy(note)
	if moved {
		// Keep the machine the task was first placed on across repeated moves; each move refreshes the landing and the
		// health-check reason. Names, not ids, go on the card/receipt (#2271).
		if t.Reroute == nil {
			t.Reroute = &PlanReroute{FromMachine: firstNonEmpty(m.Name, m.ID)}
		}
		t.Reroute.ToMachine = firstNonEmpty(alt.Name, alt.ID)
		t.Reroute.Reason = note
		t.Machine, t.Cwd = alt.ID, rehome(t.Cwd, m, alt)
	}
	return moved
}

// noteWhy adds note to t's why (once), in brackets after the assistant's reason: the card shows it.
func (t *PlanTask) noteWhy(note string) {
	if note != "" && !strings.Contains(t.Why, note) {
		t.Why = strings.TrimSpace(t.Why + " (" + note + ")")
	}
}

// fitTaskLauncher holds t's resolved launcher (params, resolveStart's) to what m has (fitLauncher): a default m lacks
// is swapped for one it has, which t then names, without a tier that no longer applies, and its why says so.
func (s *Service) fitTaskLauncher(ctx context.Context, t *PlanTask, m Device, own startPick, params map[string]any) error {
	note, err := s.fitLauncher(ctx, m, own, params)
	if err != nil || note == "" {
		return err
	}
	t.Launcher, _ = params["launcher"].(string)
	t.Model, _ = params["model"].(string)
	t.Effort, _ = params["effort"].(string)
	t.Tier = ""
	t.noteWhy(note)
	return nil
}

// taskDirectoryExists checks the mapped directory before a flexible task changes machines. Missing, denied or
// unreadable paths (including a node too old to answer fs.stat) keep that machine out of the detour.
func (s *Service) taskDirectoryExists(ctx context.Context, machine, dir string) bool {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var res struct {
		Stats []struct {
			Path string `json:"path"`
			Kind string `json:"kind"`
		} `json:"stats"`
	}
	if err := s.call(ctx, machine, "fs.stat", map[string]any{"paths": []string{dir}}, &res); err != nil {
		return false
	}
	return len(res.Stats) == 1 && res.Stats[0].Path == dir && res.Stats[0].Kind == "dir"
}

// startTaskAgent starts the agent for t on its machine (another when its launcher does not work there: t says which)
// and answers the machine's id and the session's: the machine it reached is the one the agent is linked to (looking it
// up again after the start could find another, or none). A failed start counts against the launcher there.
func (s *Service) startTaskAgent(ctx context.Context, t *PlanTask, retry bool) (mid, sid string, err error) {
	t.normalizeReadonlyPermission()
	if err := t.normalizeMachinePolicy(); err != nil {
		return "", "", err
	}
	params := map[string]any{"launcher": t.Launcher, "model": t.Model, "effort": t.Effort, "tier": t.Tier}
	own, err := s.resolveStart(ctx, params)
	if err != nil {
		return "", "", err
	}
	if t.defaulted || t.Launcher == "" {
		own.launcher, t.defaulted = "", true // filled in from a default, by this try or an earlier one
	}
	t.Launcher, _ = params["launcher"].(string)
	t.Model, _ = params["model"].(string)
	t.Effort, _ = params["effort"].(string)
	s.healthyPlace(ctx, t)
	m, ok := s.machineByName(ctx, t.Machine)
	if !ok {
		return "", "", fmt.Errorf("unknown machine %q", t.Machine)
	}
	if !m.Reachable {
		return "", "", machineOffline(m.Name)
	}
	// On the computer it landed on (a flexible task may have moved): a default it lacks gives way to one it has, a
	// launcher the plan named that it lacks is refused before any tab opens (#2640). Before the health check below,
	// which is then about the launcher that will run.
	if err := s.fitTaskLauncher(ctx, t, m, own, params); err != nil {
		return "", "", err
	}
	if t.MachinePolicy == machineFixed {
		// Only an actual terminal start can recheck a repaired terminal shell.
		// An explicit retry keeps that existing recovery path on the fixed target;
		// login-shell checks and authentication gates are not bypassed.
		if r, bad := s.launcherUnusable(ctx, m.ID, s.startLauncher(ctx, t.Launcher)); bad && !(retry && r.Kind == errKindNotInTerminal) {
			return "", "", i18n.Err("assistant-tasks.fixed_launcher_unhealthy", i18n.Args{
				"machine": m.ID, "launcher": t.Launcher, "reason": r.Error,
			})
		}
	}
	prompt := t.Prompt
	if len(t.DoneWhen) > 0 { // what the supervisor will check (supervise.go)
		prompt += "\n\n" + prompts.RenderTaskDoneWhen(t.DoneWhen)
	}
	if !t.Readonly && s.doneNeedsDelivery(ctx) { // what done means: live and checked (delivery.go)
		prompt += "\n\n" + prompts.RenderTaskDelivery()
	}
	// The project background, and the todo's decisions (a task started again may have some), go before the prompt
	// (#2820, #1618); the tab is named from the title.
	prompt = s.withProjectContext(ctx, m.ID, t.Cwd, s.withTodoDecisions(ctx, t.TodoID, prompt))
	// The user's attachments end it, copied here when this is not the assistant's computer (#2437): last, so the
	// pictures' <attached_images> block ends the message as it does one sent straight to an agent.
	if len(t.Attachments)+len(t.Mentioned) > 0 {
		prompt = withAttachments(prompt, s.handAttachments(ctx, t.AttachFrom, m.ID, t.Attachments, t.Mentioned))
	}
	params["cwd"], params["prompt"], params["tab_name"] = t.Cwd, prompt, s.tabName(ctx, t.Title)
	if t.Model != "" {
		params["model"] = t.Model
	}
	mode := t.Permission
	if mode == "" { // a plan saved before task_plan settled it, or a task started another way (tasks_permission.go)
		mode = s.taskPermission(ctx, m.ID, nil)
	}
	params["permission_mode"] = mode
	if t.Ultracode != nil && *t.Ultracode {
		params["ultracode"] = true
	}
	if t.inPane != "" { // a retry in the tab an earlier try did not start in (start_remedy.go)
		params["in_pane"] = t.inPane
	}
	var res struct {
		Session proto.Session `json:"session"`
	}
	picked, _ := params["launcher"].(string)
	if err := s.call(ctx, m.ID, DeviceSessionStart, params, &res); err != nil {
		s.recordStartFailure(ctx, m.ID, s.startLauncher(ctx, picked), err)
		return "", "", err
	}
	if res.Session.SessionID == "" || res.Session.MachineID != "" && res.Session.MachineID != m.ID {
		return "", "", &rpc.Error{Code: "identity_conflict", Message: i18n.T(s.lang(ctx), "assistant-tasks.start_identity_conflict")}
	}
	if res.Session.StartError != "" || res.Session.Starting {
		observed := res.Session
		observed.Launcher = s.startLauncher(ctx, picked)
		s.followStarts(ctx, DeviceSessions{MachineID: m.ID, Sessions: []proto.Session{observed}})
		if observed.StartError != "" {
			return "", "", sessionStartErr(observed)
		}
	}
	if !res.Session.Starting {
		s.recordStartSuccess(ctx, m.ID, s.startLauncher(ctx, picked))
	}
	return m.ID, res.Session.SessionID, nil
}

// updatePlanTask keeps the machine, directory and why task i of started plan planID runs with now.
func (s *Service) updatePlanTask(ctx context.Context, planID string, i int, t PlanTask) {
	if err := s.patchPlanTask(ctx, planID, i, func(pt *PlanTask) {
		pt.Machine, pt.Cwd, pt.Why = t.Machine, t.Cwd, t.Why
		pt.Reroute = t.Reroute
		pt.Launcher, pt.Model, pt.Effort, pt.Tier = t.Launcher, t.Model, t.Effort, t.Tier
	}); err != nil {
		s.logf("plan %s: %v", planID, err)
	}
}
