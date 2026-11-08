package extmgr

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connector"
)

// What the assistant asks of the connectors and the extensions for its directory (app.call connectors.*,
// plugins.*), answered here in its process; the hub answered it before (its assistant_apps.go).

func (h *Manager) assistantAppCall(ctx context.Context, p assistantwire.AppCall) (any, error) {
	var a struct {
		MachineID string   `json:"machine_id"`
		Names     []string `json:"names"`
		TZ        string   `json:"tz"`
	}
	if len(p.Params) > 0 {
		if err := json.Unmarshal(p.Params, &a); err != nil {
			return nil, err
		}
	}
	switch p.AppID + " " + p.Method {
	case assistantwire.AppConnectors + " " + assistantwire.ConnectorsSignedIn:
		signedIn := h.signedInConnectors(ctx)
		clis := map[string]string{}
		for _, id := range signedIn {
			if c := h.connectors.Get(id); c != nil && c.Manifest().CLI != "" {
				clis[c.Manifest().CLI] = id
			}
		}
		return map[string]any{"connectors": signedIn, "clis": clis, "lark_cli": h.cfg != nil && h.cfg.Lark.CLI != nil}, nil
	case assistantwire.AppConnectors + " " + assistantwire.ConnectorsSnapshots:
		return map[string]any{"snapshots": h.connectorSnapshots(ctx, a.TZ)}, nil
	case assistantwire.AppPlugins + " " + assistantwire.PluginsAssistant:
		return map[string]any{"instructions": h.extensions.extensionInstructions(), "skills": h.extensions.extensionSkillsFor(a.MachineID)}, nil
	case assistantwire.AppPlugins + " " + assistantwire.PluginsSkillsNeed:
		if v, ok := h.extensions.skillsSent.Load(a.MachineID); ok {
			for _, n := range a.Names {
				v.(*sync.Map).Delete(n)
			}
		}
		return map[string]any{}, nil
	}
	return nil, errNoAppMethod
}

// signedInConnectors are the connectors that are ok or rate limited (switched on and signed in).
func (h *Manager) signedInConnectors(ctx context.Context) []string {
	out := []string{}
	if h.connectors == nil {
		return out
	}
	for _, c := range h.connectors.All() {
		if st := connector.StatusOf(ctx, c).State; st == connector.StateOK || st == connector.StateRateLimited {
			out = append(out, c.ID())
		}
	}
	return out
}

// connectorSnapshots are the live states of the signed-in connectors that keep one (connector.Snapshot: a chat service
// through a plugin), asked at once and waited for 3 seconds; those that do not answer in time are left out
// (connectors.snapshots; tz is the user's zone, "" this machine's guess).
func (h *Manager) connectorSnapshots(ctx context.Context, tz string) []connector.SnapshotData {
	out := []connector.SnapshotData{}
	if h.connectors == nil {
		return out
	}
	if tz == "" {
		tz = h.userLocation().String()
	}
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	var mu sync.Mutex
	var wg sync.WaitGroup
	for _, c := range h.connectors.All() {
		sn, ok := connector.As[connector.Snapshot](c)
		if !ok || connector.StatusOf(ctx, c).State != connector.StateOK {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			d, err := sn.Snapshot(ctx, tz)
			if err != nil {
				return
			}
			mu.Lock()
			out = append(out, d)
			mu.Unlock()
		}()
	}
	wg.Wait()
	return out
}

// githubLogin is the GitHub connector's account (the persona tiers' level); known false while it has not said yet. It
// is whichever connector holds the id, the hub's own or an extension's that took it over, read through its status
// (docs/extensions/extensions.md "GitHub: from hub to extension"): only the hub's type used to count, so with the
// extension's the tiers fell to the lowest.
func (h *Manager) githubLogin(ctx context.Context) (login string, known bool) {
	acc, ok := connector.As[connector.Accounts](h.connectors.Get("github"))
	if !ok {
		return "", true
	}
	st := acc.Status(ctx)
	if st.Account != "" {
		return strings.TrimSpace(st.Account), true
	}
	return "", st.State != ""
}
