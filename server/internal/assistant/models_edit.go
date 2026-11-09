package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

type modelPatch struct {
	Revision *int64          `json:"revision"`
	Provider json.RawMessage `json:"provider"`
	Model    json.RawMessage `json:"model"`
	Effort   json.RawMessage `json:"effort"`
	Runtime  json.RawMessage `json:"runtime"`
}

func patchModel(b ModelBinding, in modelPatch) (ModelBinding, error) {
	if in.Provider != nil {
		var provider string
		if string(in.Provider) != "null" {
			if err := json.Unmarshal(in.Provider, &provider); err != nil {
				return b, i18n.Err("assistant-models.err_provider_type")
			}
		}
		if provider != b.Provider {
			b.Model, b.Effort = "", ""
		}
	}
	for _, f := range []struct {
		raw json.RawMessage
		dst *string
	}{{in.Provider, &b.Provider}, {in.Model, &b.Model}, {in.Effort, &b.Effort}, {in.Runtime, &b.Runtime}} {
		if f.raw == nil {
			continue
		}
		if string(f.raw) == "null" {
			*f.dst = ""
			continue
		}
		var v string
		if err := json.Unmarshal(f.raw, &v); err != nil {
			return b, i18n.Err("assistant-models.err_field_type")
		}
		if v == "" || strings.TrimSpace(v) != v || strings.ContainsAny(v, "\r\n\x00") || len(v) > 512 {
			return b, i18n.Err("assistant-models.err_field_invalid")
		}
		*f.dst = v
	}
	if b.Runtime != "" && !slices.Contains(assistantRuntimes, b.Runtime) {
		return b, i18n.Err("assistant-models.err_runtime_invalid")
	}
	if b.Runtime == proto.AssistantRuntimeCLI {
		b.Runtime = "" // the default, stored as none: an explicit cli fingerprints like no choice
	}
	return b, nil
}

func (s *Service) validateModel(ctx context.Context, p ModelPurpose, b ModelBinding, providers []ModelProvider) error {
	var provider *ModelProvider
	for i := range providers {
		if providers[i].ID == b.Provider {
			provider = &providers[i]
			break
		}
	}
	if provider == nil {
		return i18n.Err("assistant-models.err_provider_missing")
	}
	if !slices.Contains(p.ProviderIDs, b.Provider) {
		return i18n.Err("assistant-models.err_provider_unsupported")
	}
	if b.Effort != "" && !slices.Contains([]string{"low", "medium", "high", "xhigh", "max"}, b.Effort) {
		return i18n.Err("assistant-models.err_effort_unsupported")
	}
	if p.Kind == "api" {
		_, err := s.apiConfig(ctx, b)
		return err
	}
	if p.Kind == "plugin" {
		if b.Effort != "" {
			return i18n.Err("assistant-models.err_extension_effort")
		}
		if b.Model != "" {
			if provider.Status != "" {
				return errors.New(provider.Status)
			}
			if !slices.ContainsFunc(provider.Models, func(m ModelChoice) bool { return m.ID == b.Model }) {
				return i18n.Err("assistant-models.err_extension_model")
			}
		}
		return nil
	}
	if p.Kind == "launcher" {
		if len(provider.Machines) > 0 && p.MachineID != "" && !slices.Contains(provider.Machines, p.MachineID) {
			return i18n.Err("assistant-models.err_launcher_not_on_machine")
		}
		if b.Model != "" {
			machine := ""
			if _, ok := s.device(ctx, p.MachineID); ok {
				machine = p.MachineID
			} else if len(provider.Machines) > 0 {
				if _, ok := s.device(ctx, provider.Machines[0]); ok {
					machine = provider.Machines[0]
				}
			}
			if machine != "" {
				var result struct {
					Agent string `json:"agent"`
				}
				if err := s.call(ctx, machine, assistantwire.DeviceLaunchersModelCheck, s.launcherParams(ctx, machine, strings.TrimPrefix(b.Provider, "launcher."), map[string]any{"model": b.Model}), &result); err != nil {
					return i18n.Err("assistant-models.err_check_model", i18n.Args{"error": i18n.Localize(err, s.requestCtxLang(ctx))})
				}
				if p.CategoryID == "agent.new" && result.Agent != "claude" && result.Agent != "codex" && result.Agent != "agy" && result.Agent != "omp" {
					return i18n.Err("assistant-models.err_agent_launcher")
				}
				if p.CategoryID == "agent.new" && result.Agent == "codex" && b.Effort != "" {
					return i18n.Err("assistant-models.err_codex_effort")
				}
			}
		}
	}
	return nil
}

// providerModelUses are the current configurable purposes that run on provider: a provider in use is not deleted underneath them.
func (s *Service) providerModelUses(ctx context.Context, provider string) ([]string, error) {
	uses := []string{}
	ids := slices.Clone(modelConfigIDs)
	for _, x := range s.modelExtensions(ctx) {
		ids = append(ids, assistantwire.ExtensionPurposePrefix+x.StorageKey())
	}
	for _, id := range ids {
		p, err := s.modelPurposeDef(ctx, id)
		if err != nil {
			return nil, err
		}
		b, err := s.resolveModel(ctx, id)
		if err != nil {
			return nil, err
		}
		if b.Provider == provider {
			uses = append(uses, p.Name)
		}
	}
	return uses, nil
}

// triageInUse refuses a change that leaves inbox.triage uncallable while plugins that need it are on: 422
// triage_model_in_use, naming them (not 409: the apps read a 409 from model-config as an edit conflict and would hide
// the message; a 422 they show as it is). nil when the change may go (docs/extensions/extensions.md "General inbox rules").
func (s *Service) triageInUse(ctx context.Context, id string, effective ModelBinding) (map[string]any, error) {
	if id != "inbox.triage" || s.modelUsable(ctx, effective) {
		return nil, nil
	}
	var res struct {
		Names []string `json:"names"`
	}
	if err := s.appCall(ctx, AppPlugins, assistantwire.PluginsNeedTriage, map[string]any{}, &res); err != nil {
		return nil, err
	}
	if len(res.Names) == 0 {
		return nil, nil
	}
	l := s.requestCtxLang(ctx)
	return map[string]any{"code": "triage_model_in_use", "plugins": res.Names,
		"error": i18n.T(l, "assistant-models.triage_in_use", i18n.Args{"names": strings.Join(res.Names, i18n.T(l, "assistant-models.list_separator"))})}, nil
}
