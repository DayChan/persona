package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"net/http"
	"slices"
	"strconv"
	"time"
)

// putSettings is PUT /api/v1/assistant: what the body sets changes ("-" as main clears it); a launcher is checked
// on the assistant's machine and goes into Model configuration › Main chat too.
func (s *Service) putSettings(w http.ResponseWriter, r *http.Request) {
	if !fullRole(r) {
		apierr.Write(w, http.StatusForbidden, errors.New("read-only role cannot change settings"))
		return
	}
	ctx := r.Context()
	var in struct {
		Settings
		Permission json.RawMessage `json:"default_permission_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		apierr.Write(w, http.StatusBadRequest, err)
		return
	}
	if in.Permission != nil {
		if err := json.Unmarshal(in.Permission, &in.DefaultPermissionMode); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Permission != nil && !slices.Contains(proto.PermissionModes, in.DefaultPermissionMode) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-schedule.bad_permission_mode", i18n.Args{"mode": strconv.Quote(in.DefaultPermissionMode)}))
		return
	}
	if in.Permission != nil && !s.explicitSettingsTarget(w, r) {
		return
	}
	machine := s.settings(ctx).MachineID
	if in.MachineID != "" {
		if _, ok := s.device(ctx, in.MachineID); !ok {
			apierr.Write(w, http.StatusBadRequest, errors.New("unknown machine"))
			return
		}
		machine = in.MachineID
	}
	if s.isWorkMachine(machine) {
		apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-start.work_machine"))
		return
	}
	if in.Launcher != "" {
		if err := s.checkLauncher(ctx, machine, in.Launcher); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if d := in.RecordingDays; d != nil && (*d < minRecordingDays || *d > maxRecordingDays) {
		apierr.Write(w, http.StatusBadRequest, errors.New("recording_days must be 1–365"))
		return
	}
	if p := in.ReviewSamplePercent; p != nil && (*p < 0 || *p > 100) {
		apierr.Write(w, http.StatusBadRequest, errors.New("review_sample_percent must be 0–100"))
		return
	}
	if d := in.IdleCloseHours; d != nil && (*d < 0 || *d > maxIdleCloseHours) {
		apierr.Write(w, http.StatusBadRequest, errors.New("idle_close_hours must be 0–8760"))
		return
	}
	if in.Timezone != "" {
		if _, err := time.LoadLocation(in.Timezone); err != nil {
			apierr.Write(w, http.StatusBadRequest, err)
			return
		}
	}
	if in.Launcher != "" {
		rec, err := s.modelRecord(ctx, "assistant")
		if err == nil {
			rec.Binding = mergeModel(rec.Binding, ModelBinding{Provider: "launcher." + in.Launcher})
			err = s.saveModel(ctx, "assistant", rec)
		}
		if err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
	}
	if in.Main != "" && in.Main != "-" {
		// The user's from now on, as a main conversation started as one (startMain): no extension keeps its mode.
		if err := s.originMadeMain(ctx, in.Main); err != nil {
			apierr.Write(w, http.StatusInternalServerError, err)
			return
		}
	}
	launcher := s.launcherNow(ctx)
	if _, err := s.updateSettings(ctx, func(v *Settings) error {
		*v = s.effective(ctx, *v) // saved as they are in effect, as the hub saved them
		v.MachineID, v.Launcher = machine, launcher
		if in.Launcher != "" {
			v.Launcher = in.Launcher
		}
		if in.Main == "-" {
			v.Main = ""
		} else if in.Main != "" {
			v.Main = in.Main
		}
		if in.Timezone != "" {
			v.Timezone = in.Timezone
		}
		if in.Notes != nil {
			v.Notes = in.Notes
		}
		if in.OwnerAliases != nil {
			v.OwnerAliases = ownerAliases(in.OwnerAliases)
		}
		if in.MainTasksQuiet != nil {
			v.MainTasksQuiet = in.MainTasksQuiet
		}
		if in.StartRequestedTasks != nil {
			v.StartRequestedTasks = in.StartRequestedTasks
		}
		if in.RecordingDays != nil {
			v.RecordingDays = in.RecordingDays
		}
		if in.IdleCloseHours != nil {
			v.IdleCloseHours = in.IdleCloseHours
		}
		if in.DoneNeedsDelivery != nil {
			v.DoneNeedsDelivery = in.DoneNeedsDelivery
		}
		if in.ReviewSamplePercent != nil {
			v.ReviewSamplePercent = in.ReviewSamplePercent
		}
		if in.DefaultPermissionMode != "" {
			v.DefaultPermissionMode = in.DefaultPermissionMode
		}
		return nil
	}); err != nil {
		apierr.Write(w, http.StatusInternalServerError, err)
		return
	}
	s.settingsChanged(ctx)
	apierr.WriteJSON(w, s.view(ctx, false))
}

// checkLauncher: the assistant runs Claude Code (claude -p), Codex (codex app-server) or an agent runtime that says it
// can (its launcher has assistant: docs/integrations/runtimes.md "Assistant"); anything else would fail every message. One that can't
// be probed now goes.
func (s *Service) checkLauncher(ctx context.Context, machine, launcher string) error {
	if launcher == "claude" {
		return nil
	}
	if _, ok := s.device(ctx, machine); !ok || s.runtimeAssistantLauncher(ctx, machine, launcher) {
		return nil
	}
	var res struct {
		Detail proto.LauncherDetail `json:"detail"`
	}
	pctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	err := s.call(pctx, machine, DeviceLaunchersProbe, map[string]any{"name": launcher}, &res)
	cancel()
	if err == nil && res.Detail.Agent != "" && res.Detail.Agent != "claude" && res.Detail.Agent != "codex" {
		return i18n.Err("assistant-profile.launcher_agent", i18n.Args{"launcher": launcher, "agent": res.Detail.Agent})
	}
	return nil
}

// runtimeAssistantLauncher: launcher is one an agent runtime on machine offers to run the assistant (the node's
// launchers.list says assistant), not a login-shell launcher to probe.
func (s *Service) runtimeAssistantLauncher(ctx context.Context, machine, launcher string) bool {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	var res struct {
		Launchers []proto.Launcher `json:"launchers"`
	}
	if err := s.call(ctx, machine, DeviceLaunchersList, map[string]any{}, &res); err != nil {
		return false
	}
	return slices.ContainsFunc(res.Launchers, func(l proto.Launcher) bool { return l.Name == launcher && l.Assistant })
}
