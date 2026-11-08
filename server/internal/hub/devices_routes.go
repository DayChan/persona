package hub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/devicewire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// metricsRoutesRelayed relays the monitoring's routes (devicewire.MetricsRoutes: the machines' samples, their history,
// the token usage) to the manager, which keeps them.
func (h *Hub) metricsRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.MetricsRoutes {
		by := h.byRequest
		if strings.Contains(pattern, "/machines/{id}/") {
			by = h.byMachine
		}
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			_ = h.waitDevices(r.Context()) // the hub just started: the manager is on its way (bounded); else 503 at once
			h.forwardOwned(w, r, by)
		})
	}
}

// usageRoutesRelayed relays the subscription usage windows' route (devicewire.UsageRoutes) to the manager, which
// asks the machines it connects and the account-pool gateways (docs/server/usage-windows.md); 503 assistant_offline while
// it is away.
func (h *Hub) usageRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.UsageRoutes {
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			_ = h.waitDevices(r.Context()) // the hub just started: the manager is on its way (bounded); else 503 at once
			h.forwardOwned(w, r, h.byRequest)
		})
	}
}

// adminUsageRoutesRelayed relays the admin UI's usage sources page (devicewire.AdminUsageRoutes) to the manager. mux
// is the admin's signed-in routes: the hub has checked the session (and the CSRF token of a write) before, and tells
// the manager so (assistantwire.AdminHeader); the apps' API has no such route.
func (h *Hub) adminUsageRoutesRelayed(mux *http.ServeMux) {
	for _, pattern := range devicewire.AdminUsageRoutes {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			_ = h.waitDevices(r.Context())
			if id, ok := h.owner(w, r, h.byRequest); ok { // the manager it names, else the default: its usage sources
				h.forwardAdminHTTP(w, r, id)
			}
		})
	}
}

// deviceRoutesRelayed relays the apps' requests on the routes that are the manager's (devicewire.Routes: its own
// settings of each machine and what only it can do) to it, as the assistant's (forwardHTTP); while it is not
// connected they are answered 503 assistant_offline.
func (h *Hub) deviceRoutesRelayed(api *http.ServeMux) {
	for _, pattern := range devicewire.Routes {
		api.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if h.localMachine(r.PathValue("mid")) == nil {
				writeErr(w, http.StatusNotFound, i18n.Err("devices-api.unknown_machine"))
				return
			}
			_ = h.waitDevices(r.Context()) // the hub just started: the manager is on its way (bounded); else 503 at once
			h.forwardOwned(w, r, h.byMachine)
		})
	}
}

// managerHTTP asks manager id (the machine's, for a machine's route) one of its routes (devicewire.Routes) as an app
// would, for the hub's own features (persona_settings); a status that is not 2xx is an error with the body's message.
func (h *Hub) managerHTTP(ctx context.Context, id, method, path string, body any, out any) error {
	req := assistantwire.HTTPRequest{Method: method, Path: path, Header: map[string][]string{"Content-Type": {"application/json"}}}
	req.Header[assistantwire.RoleHeader] = []string{"full"}
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		req.Body = b
	}
	var res assistantwire.HTTPResponse
	if err := h.callBus(ctx, id, assistantwire.MethodHTTP, req, &res); err != nil {
		if errors.Is(err, errBusOffline) {
			return errAssistantOffline
		}
		return err
	}
	if res.Status >= 300 {
		if why := apierr.Describe(res.Body); why != "" { // with the cause a localized summary moved to detail (#5728)
			return errors.New(why)
		}
		return fmt.Errorf("%s %s: %d", method, path, res.Status)
	}
	if out != nil {
		return json.Unmarshal(res.Body, out)
	}
	return nil
}

// claudeDirsState is what GET /api/v1/machines/{mid}/claude-dirs answers (the manager's, claudedirs.go there).
type claudeDirsState struct {
	Dirs    []proto.ClaudeDir `json:"dirs"`
	Blocked []string          `json:"blocked"`
	Home    string            `json:"home,omitempty"`
	Live    bool              `json:"live"`
}

func (m *Machine) claudeDirsState(ctx context.Context) claudeDirsState {
	var st claudeDirsState
	if err := m.hub.managerHTTP(ctx, m.hub.machineOwner(m.cfg.ID), http.MethodGet, "/api/v1/machines/"+m.cfg.ID+"/claude-dirs", nil, &st); err != nil {
		m.hub.logf("[%s] claude dirs: %v", m.cfg.ID, err)
	}
	return st
}

func (m *Machine) blockedClaudeDirs(ctx context.Context) []string {
	return m.claudeDirsState(ctx).Blocked
}

// readBlockedClaudeDirs is blockedClaudeDirs with the error, for changing the list: a failed read is not "none
// blocked", or the PUT that follows would replace every pattern with just the one changed.
func (m *Machine) readBlockedClaudeDirs(ctx context.Context) ([]string, error) {
	var st claudeDirsState
	if err := m.hub.managerHTTP(ctx, m.hub.machineOwner(m.cfg.ID), http.MethodGet, "/api/v1/machines/"+m.cfg.ID+"/claude-dirs", nil, &st); err != nil {
		return nil, fmt.Errorf("read blocked claude dirs: %w", err)
	}
	return st.Blocked, nil
}

func (m *Machine) setBlockedClaudeDirs(ctx context.Context, patterns []string) error {
	return m.hub.managerHTTP(ctx, m.hub.machineOwner(m.cfg.ID), http.MethodPut, "/api/v1/machines/"+m.cfg.ID+"/claude-dirs/blocked",
		map[string]any{"blocked": patterns}, nil)
}
