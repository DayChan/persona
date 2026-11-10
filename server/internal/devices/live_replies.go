package devices

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// Live replies (#1309, docs/integrations/claude-integration.md "Live replies"): the Claude sessions Persona starts on a
// machine load the plugin with Claude Code's MessageDisplay hook, so their replies reach the apps as Claude shows them.
// It costs the terminal on that computer something (replies show a batch of lines at a time, each once the hook ran),
// so Settings › Claude Code has a switch per machine. On unless the user turned it off; a node of an older hub, which
// never says, keeps it on too.

// liveRepliesKey is where a machine's choice is kept (the hub's settings table).
func liveRepliesKey(machine string) string { return "claude_live_replies/" + machine }

// liveReplies is whether the machine's Claude starts stream their replies: what the user chose, else on.
func (m *Machine) liveReplies(ctx context.Context) bool {
	if m.d.opts.Store != nil {
		var on bool
		if ok, err := m.d.opts.Store.Setting(ctx, liveRepliesKey(m.cfg.ID), &on); ok && err == nil {
			return on
		}
	}
	return true
}

// setLiveReplies stores the machine's choice and hands it to the node, as configureNode does when it connects. It
// reaches the sessions started from then on; one already running keeps what it started with until Claude restarts.
func (m *Machine) setLiveReplies(ctx context.Context, on bool) error {
	if m.d.opts.Store == nil {
		return i18n.Err("devices-api.no_store")
	}
	if err := m.d.opts.Store.PutSetting(ctx, liveRepliesKey(m.cfg.ID), on); err != nil {
		return err
	}
	call, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	// What is stored when this goes out, not on: of two flips at once, the node ends on the one kept. m.call adds the
	// directories (withClaudeDirs), so the node keeps the ones it was given.
	if err := m.call(call, "node.configure", map[string]any{"claude_live_replies": m.liveReplies(call)}, nil); err != nil {
		m.d.logf("[%s] configure live replies: %v", m.cfg.ID, err)
	}
	return nil
}

// liveRepliesState is what GET / PUT /api/v1/machines/{mid}/claude-live-replies answer: the setting in force.
type liveRepliesState struct {
	On bool `json:"on"`
}

// getLiveReplies: GET /api/v1/machines/{mid}/claude-live-replies.
func (d *Devices) getLiveReplies(w http.ResponseWriter, r *http.Request, m *Machine) {
	writeJSON(w, liveRepliesState{On: m.liveReplies(r.Context())})
}

// putLiveReplies: PUT /api/v1/machines/{mid}/claude-live-replies {"on": bool}, the switch in Settings › Claude Code.
// Stored while the machine is offline too (sent when it connects).
func (d *Devices) putLiveReplies(w http.ResponseWriter, r *http.Request, m *Machine) {
	var in struct {
		On *bool `json:"on"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil || in.On == nil {
		writeErr(w, http.StatusBadRequest, i18n.Err("devices-api.need_on"))
		return
	}
	if err := m.setLiveReplies(r.Context(), *in.On); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, liveRepliesState{On: m.liveReplies(r.Context())})
}
