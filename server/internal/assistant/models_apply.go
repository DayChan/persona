package assistant

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/textfmt"
)

// ---- what a node call runs on ----

// applyModel fills params (a node call's: session.start, assistant.send…) with what purpose id runs on; a launcher,
// model or effort already in params is this call's own pick. The permission mode is left as it is.
func (s *Service) applyModel(ctx context.Context, id string, params map[string]any) error {
	b, err := s.resolveModel(ctx, id)
	if err != nil {
		return err
	}
	if name, _ := params["launcher"].(string); name != "" && launcherProvider(name) != b.Provider {
		b = ModelBinding{Provider: launcherProvider(name), Runtime: b.Runtime}
	}
	if b.Model != "" && strings.HasPrefix(b.Provider, "launcher.") && !proto.ValidModelID(b.Model) {
		// Configured before a launcher's model was held to the model id rule (#1644; Model configuration refuses it as
		// an edit now): the launcher's own default, rather than every run of this purpose failing, the main chat's too.
		s.logf("model config %s: %q is no model id, running %s's default", id, textfmt.Clip(b.Model, 80), b.Provider)
		b.Model = ""
	}
	if model, _ := params["model"].(string); strings.TrimSpace(model) != "" {
		b.Model = strings.TrimSpace(model) // this call's pick: finishModel refuses one that is no model id
	} else {
		delete(params, "model") // none picked ("" or blanks): the binding's
	}
	if effort, _ := params["effort"].(string); effort != "" {
		b.Effort = effort
	}
	return s.finishModel(ctx, id, params, b)
}

// applyPickedModel is applyModel for a launcher/model picked for one send (the chat's menu): with no model the
// launcher's own default stays, instead of the purpose's configured one.
func (s *Service) applyPickedModel(ctx context.Context, params map[string]any) error {
	name, _ := params["launcher"].(string)
	if name == "" {
		return nil
	}
	b := ModelBinding{Provider: launcherProvider(name)}
	if m, _ := params["model"].(string); strings.TrimSpace(m) != "" {
		b.Model = strings.TrimSpace(m)
	} else {
		delete(params, "model") // the launcher's own default
	}
	if e, _ := params["effort"].(string); e != "" {
		b.Effort = e
	}
	// A pick is a launcher and model for one message; the runtime stays the main chat's.
	configured, err := s.resolveModel(ctx, "assistant")
	if err != nil {
		return err
	}
	b.Runtime = configured.Runtime
	return s.finishModel(ctx, "assistant", params, b)
}

// finishModel puts a final binding into params: the launcher name, the model / effort set, the execution fingerprint.
// A launcher's model goes to the agent as --model, so it has to be a model id (#1644): a call's own pick that is not
// one is refused here (a configured one that is not was dropped by applyModel).
func (s *Service) finishModel(ctx context.Context, id string, params map[string]any, b ModelBinding) error {
	name, ok := strings.CutPrefix(b.Provider, "launcher.")
	if !ok {
		if strings.HasPrefix(id, "plugin.") {
			params["model"] = b.Model
			return nil
		}
		return i18n.Err("assistant-models.err_not_launcher")
	}
	if err := checkModelID(b.Model); err != nil {
		return err
	}
	for _, provider := range s.storedLaunchers(ctx) {
		// A direct API provider speaking Messages starts Claude on its gateway (node, contract #2455 §2);
		// other direct API protocols cannot.
		if provider.Name == name && provider.Kind == "api" && !messagesAPI(provider.API) {
			return i18n.Err("assistant-models.err_api_as_launcher")
		}
	}
	if b.Runtime != "" {
		params["runtime"] = b.Runtime
	} else {
		delete(params, "runtime") // the CLI: nothing a caller left
	}
	params["launcher"] = name
	if b.Model != "" {
		params["model"] = b.Model
	}
	if b.Effort != "" {
		params["effort"] = b.Effort
	}
	raw, _ := json.Marshal(b)
	sum := sha256.Sum256(append([]byte(s.modelBindingFingerprint(ctx, b)), raw...))
	params["model_fingerprint"] = hex.EncodeToString(sum[:])
	return nil
}
