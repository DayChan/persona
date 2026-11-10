package assistant

import (
	"context"
	"slices"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/proto"
)

// The permission mode of a planned task that task_plan left unset (#3026). The hub settles it, so the plan card shows
// the mode that runs (#1897): the manager's default permission mode (Settings › Assistant), else the default mode in
// Claude Code's settings on the computer the task lands on (Settings › Claude Code, permissions.defaultMode: what an
// Agent started from the Agents page with no mode runs in), else Ask every step. Never left unset: the start would
// pass no --permission-mode, and a launcher's own flags would decide without the card saying so.

// claudeModeTimeout bounds reading a computer's Claude Code settings (one file on the node): a node that does not
// answer gives Ask every step rather than holding up the plan.
const claudeModeTimeout = 3 * time.Second

// taskPermission is the mode of a task with no permission of its own on machine (an id or a name). modes, when not
// nil, keeps each computer's answer for the rest of one plan.
func (s *Service) taskPermission(ctx context.Context, machine string, modes map[string]string) string {
	if mode := s.settings(ctx).DefaultPermissionMode; mode != "" {
		return mode
	}
	mode, known := modes[machine]
	if !known {
		mode = s.claudeDefaultMode(ctx, machine)
		if modes != nil {
			modes[machine] = mode
		}
	}
	if mode == "" {
		return "default"
	}
	return mode
}

// claudeDefaultMode is permissions.defaultMode in Claude Code's settings on machine when it is a mode Persona knows;
// "" when it is not set, is another value, or the computer is offline or its node older than the setting.
func (s *Service) claudeDefaultMode(ctx context.Context, machine string) string {
	m, ok := s.machineByName(ctx, machine)
	if !ok || !m.Reachable {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, claudeModeTimeout)
	defer cancel()
	var res struct {
		Settings []struct {
			Key   string `json:"key"`
			Value any    `json:"value"`
		} `json:"settings"`
	}
	if err := s.call(ctx, m.ID, assistantwire.DeviceClaudeSettings, map[string]any{}, &res); err != nil {
		s.logf("tasks: %s: claude settings: %v", m.ID, err)
		return ""
	}
	for _, setting := range res.Settings {
		if mode, _ := setting.Value.(string); setting.Key == proto.ClaudeDefaultModeSetting && slices.Contains(proto.PermissionModes, mode) {
			return mode
		}
	}
	return ""
}
