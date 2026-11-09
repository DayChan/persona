package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
)

// modelPurpose is purpose id as Settings › Model configuration shows it: what it is set to, what it runs on, and how that stands.
func (s *Service) modelPurpose(ctx context.Context, id string, providers []ModelProvider) (ModelPurpose, error) {
	p, err := s.modelPurposeDef(ctx, id)
	if err != nil {
		return p, err
	}
	recordID := modelRecordID(p)
	r, err := s.modelRecord(ctx, recordID)
	if err != nil {
		return p, err
	}
	p.Override, p.Revision = r.Binding, r.Revision
	readOnly := recordID != id
	if readOnly {
		p.Override = ModelBinding{}
	}
	p.Effective, err = s.resolveModel(ctx, id)
	if err != nil {
		return p, err
	}
	base, from, err := s.modelBaseline(ctx, p)
	if err != nil {
		return p, err
	}
	p.InheritedFrom = inheritedLabel(s.requestCtxLang(ctx), p, base, from, providers)
	stored := s.storedLaunchers(ctx)
	p.Fields = []string{"provider", "model"}
	p.ProviderIDs = []string{}
	for _, v := range providers {
		compatible := v.Kind == p.Kind
		if p.Kind == "launcher" && v.Kind == "api" { // a direct Messages API provider starts Claude on its gateway (#2455)
			compatible = messagesAPI(v.API)
		}
		if p.Kind == "api" && v.Kind == "launcher" {
			for _, l := range stored {
				if v.ID == "launcher."+l.Name {
					compatible = messagesAPI(l.API)
					break
				}
			}
		}
		if p.Kind == "plugin" {
			compatible = v.ID == p.ID
		}
		if compatible && p.Kind == "launcher" && len(v.Machines) > 0 && p.MachineID != "" {
			compatible = slices.Contains(v.Machines, p.MachineID)
		}
		if compatible {
			p.ProviderIDs = append(p.ProviderIDs, v.ID)
		}
		if v.ID == p.Effective.Provider {
			if p.Effective.Model == "" {
				p.Effective.Model = v.Model
			}
			if p.Kind == "api" && v.API == "responses" {
				p.Fields = append(p.Fields, "effort")
			}
		}
	}
	if p.Kind == "launcher" {
		p.Fields = append(p.Fields, "effort")
	}
	if len(p.Runtimes) > 0 {
		p.Fields = append(p.Fields, "runtime")
	}
	l := s.requestCtxLang(ctx)
	p.Status = modelUnverified
	if !slices.Contains(p.ProviderIDs, p.Effective.Provider) {
		p.Status = mc(l, "status_provider_incompatible")
		if p.Kind == "api" && strings.HasPrefix(p.Effective.Provider, "legacy.") && s.legacyModelConfig(ctx, p.Effective.Provider).BaseURL == "" {
			p.Status = mc(l, modelNoAPIStatus)
		}
	} else if p.Kind == "api" && p.Effective.Model == "" {
		p.Status = mc(l, "status_no_model")
	} else if p.Kind == "api" {
		if config, e := s.apiConfig(ctx, p.Effective); e != nil {
			p.Status = i18n.Localize(e, l)
		} else if config.APIKey == "" && config.APIKeyFile == "" {
			p.Status = mc(l, "status_no_credentials")
		} else if pause := ai.Held(config); pause != nil {
			p.Status, p.Limit = pause.Text(), pause
		}
	} else if p.Kind == "plugin" {
		for _, provider := range providers {
			if provider.ID == p.Effective.Provider && provider.Status != "" {
				p.Status = provider.Status
				break
			}
		}
	} else if p.Kind == "launcher" && p.MachineID != "" {
		if d, ok := s.device(ctx, p.MachineID); ok {
			if !d.Online {
				p.Status = mc(l, "status_machine_offline")
			} else if d.Version != "" && !d.ModelConfiguration {
				p.Status = mc(l, "status_node_outdated")
			}
		}
	}
	if readOnly {
		p.Fields = []string{}
		for _, category := range modelPurposes {
			if category.ID == recordID {
				p.InheritedFrom = purposeName(l, category.ID)
				break
			}
		}
	}
	return p, nil
}

func (s *Service) modelConfigRoutes() {
	s.handle("GET /api/v1/model-config", func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		providers := s.modelProviders(ctx)
		purposes := []ModelPurpose{}
		for _, id := range modelConfigIDs {
			p, err := s.modelPurpose(ctx, id, providers)
			if err != nil {
				apierr.Write(w, 500, err)
				return
			}
			purposes = append(purposes, p)
		}
		// The extensions' own (model_rpc.go), after the categories: each with its own record, like Lark /agent.
		for _, x := range s.modelExtensions(ctx) {
			p, err := s.modelPurpose(ctx, assistantwire.ExtensionPurposePrefix+x.StorageKey(), providers)
			if err != nil {
				apierr.Write(w, 500, err)
				return
			}
			purposes = append(purposes, p)
		}
		apierr.WriteJSON(w, map[string]any{"purposes": purposes, "providers": providers})
	})
	detail := func(w http.ResponseWriter, r *http.Request) {
		providers := s.modelProviders(r.Context())
		p, err := s.modelPurpose(r.Context(), r.PathValue("id"), providers)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errModelPurposeNotFound) {
				status = http.StatusNotFound
			}
			apierr.Write(w, status, err)
			return
		}
		apierr.WriteJSON(w, map[string]any{"purpose": p, "providers": providers})
	}
	s.handle("GET /api/v1/model-config/purposes/{id}", detail)
	s.handle("GET /api/v1/model-config/providers/{id}/models", s.providerModels)
	// Both verbs keep sparse updates and the same revision-checked write path.
	update := func(w http.ResponseWriter, r *http.Request) {
		var in modelPatch
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if in.Revision == nil || *in.Revision < 0 {
			apierr.Write(w, 400, i18n.Err("assistant-models.err_revision_required"))
			return
		}
		s.models.mu.Lock()
		defer s.models.mu.Unlock()
		ctx, id := r.Context(), r.PathValue("id")
		providers := s.modelProviders(ctx)
		p, err := s.modelPurpose(ctx, id, providers)
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, errModelPurposeNotFound) {
				status = http.StatusNotFound
			}
			apierr.Write(w, status, err)
			return
		}
		if len(p.Fields) == 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models.err_purpose_readonly"))
			return
		}
		if p.Revision != *in.Revision {
			apierr.Write(w, 409, errModelConflict)
			return
		}
		if in.Runtime != nil && string(in.Runtime) != "null" && len(p.Runtimes) == 0 {
			apierr.Write(w, 400, i18n.Err("assistant-models.err_runtime_assistant_only"))
			return
		}
		b, err := patchModel(p.Override, in)
		if err != nil {
			apierr.Write(w, 400, err)
			return
		}
		if p.Kind == "launcher" { // passed to an agent as --model: a model id (#1644)
			if err := checkModelID(b.Model); err != nil {
				apierr.Write(w, 400, err)
				return
			}
		}
		base, _, err := s.modelBaseline(ctx, p)
		if err != nil {
			apierr.Write(w, 500, err)
			return
		}
		effective := mergeModel(base, b)
		if body, err := s.triageInUse(ctx, id, effective); err != nil {
			apierr.Write(w, http.StatusServiceUnavailable, err)
			return
		} else if body != nil { // plugins that need it are on
			apierr.WriteBody(w, http.StatusUnprocessableEntity, body)
			return
		}
		if in.Effort != nil && string(in.Effort) != "null" && p.Kind == "api" {
			config, e := s.apiConfig(ctx, effective)
			if e != nil {
				apierr.Write(w, 400, e)
				return
			}
			if config.API != "responses" {
				apierr.Write(w, 400, i18n.Err("assistant-models.err_api_effort"))
				return
			}
		}
		if b != (ModelBinding{}) {
			if err = s.validateModel(ctx, p, effective, providers); err != nil {
				apierr.Write(w, 400, err)
				return
			}
		}
		if err = s.saveModelRecord(ctx, id, p.Revision, b); err != nil {
			status := 500
			if errors.Is(err, errModelConflict) {
				status = 409
			}
			apierr.Write(w, status, err)
			return
		}
		detail(w, r)
	}
	s.handle("PATCH /api/v1/model-config/purposes/{id}", update)
	s.handle("PUT /api/v1/model-config/purposes/{id}", update)
}
