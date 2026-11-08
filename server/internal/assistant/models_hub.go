package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/nwverse/persona/server/internal/compat"
	"net/http"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/quota"
	"github.com/nwverse/persona/server/internal/rpc"
)

// What the hub's plugins ask of the model configuration (assistantwire.MethodModels*): whether "Sorting" can be called
// (a plugin whose inbox needs it is switched on only then), which launchers have a gateway, and requests to a launcher's gateway sent with its key
// (the quota plugins, the plugins' launcher sections). The hub never gets a key or a model call.

// modelsRequest answers the hub's models.* calls (bus.go's request); false for any other method.
func (s *Service) modelsRequest(ctx context.Context, method string, params json.RawMessage) (any, bool, error) {
	var p struct {
		Purpose string         `json:"purpose"`
		Name    string         `json:"name"`
		Machine string         `json:"machine"`
		HTTP    *quota.Request `json:"http"`
	}
	switch method {
	case assistantwire.MethodExtensionIdentity:
		return map[string]bool{"supported": true}, true, nil
	case assistantwire.MethodModelsUsable, assistantwire.MethodModelsLauncherHTTP:
	case assistantwire.MethodModelsLaunchers:
		return map[string]any{"launchers": s.gatewayLaunchers(ctx)}, true, nil
	case assistantwire.MethodModelsExtensionComplete: // an extension's model.complete (model_rpc.go)
		out, err := s.extensionComplete(ctx, params)
		return out, true, err
	default:
		return nil, false, nil
	}
	if len(params) > 0 {
		if err := json.Unmarshal(params, &p); err != nil {
			return nil, true, &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + err.Error()}
		}
	}
	if method == assistantwire.MethodModelsUsable {
		b, err := s.resolveModel(ctx, p.Purpose)
		return map[string]any{"usable": err == nil && s.modelUsable(ctx, b)}, true, nil
	}
	out, err := s.launcherHTTP(ctx, p.Name, p.Machine, p.HTTP)
	return out, true, err
}

// gatewayLaunchers is models.launchers: every launcher with a gateway address, names and addresses only (no key).
func (s *Service) gatewayLaunchers(ctx context.Context) []assistantwire.GatewayLauncher {
	out := []assistantwire.GatewayLauncher{}
	for _, v := range s.launcherViews(ctx) {
		if v.BaseURL != "" {
			out = append(out, assistantwire.GatewayLauncher{Name: v.Name, BaseURL: v.BaseURL})
		}
	}
	return out
}

// launcherHTTP is models.launcher_http: without r, where launcher name's gateway is ({base_url, machine}); with r,
// r sent there with the launcher's key — here for a provider added in Persona, by the node of a machine that has a
// ~/.zshrc one (machine when it has it, else pickMachine's), so that key stays on that machine.
func (s *Service) launcherHTTP(ctx context.Context, name, machine string, r *quota.Request) (any, error) {
	var view *LauncherView
	for _, v := range s.launcherViews(ctx) {
		if v.Name == name {
			view = &v
			break
		}
	}
	if view == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeNoLauncher, Message: "no such launcher"}
	}
	if compat.Source(view.Source) == "persona" {
		for _, l := range s.storedLaunchers(ctx) {
			if l.Name != name {
				continue
			}
			if l.BaseURL == "" {
				return nil, &rpc.Error{Code: assistantwire.CodeNoGateway, Message: i18n.T(s.lang(ctx), "assistant-models_launchers.no_gateway")}
			}
			if r == nil {
				local := ""
				for _, d := range s.devices(ctx) {
					if d.Kind == "local" {
						local = d.ID
						break
					}
				}
				return map[string]any{"base_url": l.BaseURL, "machine": local}, nil
			}
			key, err := s.openKey(l.KeySealed)
			if err != nil {
				return nil, errors.New(i18n.T(s.lang(ctx), "assistant-models.err_key_decrypt"))
			}
			return quota.Do(ctx, &http.Client{Timeout: 25 * time.Second}, l.BaseURL, key, *r)
		}
		return nil, &rpc.Error{Code: assistantwire.CodeNoLauncher, Message: "no such launcher"}
	}
	id, ok := s.pickMachine(ctx, view.Machines, machine)
	if !ok {
		return nil, &rpc.Error{Code: assistantwire.CodeMachineOffline, Message: apierr.ErrOffline.Error()}
	}
	params := s.launcherParams(ctx, id, name, map[string]any{})
	if r == nil {
		var gw struct {
			BaseURL string `json:"base_url"`
		}
		if err := s.call(ctx, id, assistantwire.DeviceLaunchersHTTP, params, &gw); err != nil {
			if strings.Contains(err.Error(), "unknown method") {
				d, _ := s.device(ctx, id)
				return nil, errors.New(i18n.T(s.lang(ctx), "assistant-models_launchers.quota_node_outdated", i18n.Args{"name": d.Name}))
			}
			return nil, err
		}
		return map[string]any{"base_url": gw.BaseURL, "machine": id}, nil
	}
	params["http"] = r
	var resp quota.Response
	err := s.call(ctx, id, assistantwire.DeviceLaunchersHTTP, params, &resp)
	return resp, err
}
