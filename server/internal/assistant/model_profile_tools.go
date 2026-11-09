package assistant

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/nwverse/persona/server/internal/prompts"
)

// Profile tools expose the same operations as settings, without copying credentials into model context.
func (s *Service) toolModelProfile(ctx context.Context, self, name string, args json.RawMessage) (string, error) {
	var a struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", err
	}
	by := ProfileActor{Kind: "assistant"}
	switch name {
	case "model_profile_switch":
		if a.ID == "" {
			return "", fmt.Errorf("id is required; use model_profiles_list")
		}
		if _, err := s.applyModelProfile(ctx, a.ID, by); err != nil {
			return "", err
		}
	case "model_profile_rollback":
		if _, err := s.rollbackModelProfile(ctx, by); err != nil {
			return "", err
		}
	}
	v, err := s.modelProfilesView(ctx)
	if err != nil {
		return "", err
	}
	var result any = v
	if name == "model_profile_get" {
		i := slices.IndexFunc(v.Profiles, func(p ModelProfileView) bool { return p.ID == a.ID })
		if i < 0 {
			return "", errModelProfileNotFound
		}
		result = v.Profiles[i]
	}
	raw, err := json.Marshal(result)
	return string(raw), err
}

func (s *Service) applyStartTier(ctx context.Context, params map[string]any) error {
	tier, _ := params["tier"].(string)
	delete(params, "tier") // node runtimes need only the resolved fields
	if tier == "" {
		return nil
	}
	t, err := s.tierModel(ctx, tier)
	if err != nil {
		return err
	}
	launcher, _ := params["launcher"].(string)
	model, _ := params["model"].(string)
	effort, _ := params["effort"].(string)
	layTier(t, &launcher, &model, &effort)
	params["launcher"], params["model"], params["effort"] = launcher, model, effort
	return nil
}

// layTier lays tier t over a start's launcher, model and effort, and says whether it gave them anything: not when the
// start names a launcher of its own (withTier). An explicit different model does not inherit effort intended for the
// tier's model.
func layTier(t ModelTier, launcher, model, effort *string) bool {
	if *launcher != "" && *launcher != t.Launcher {
		return false
	}
	if *model != "" && *model != t.Model {
		t.Effort = ""
	}
	withTier(t, launcher, model, effort)
	return true
}

func (s *Service) applyTaskTier(ctx context.Context, t *PlanTask) error {
	p := map[string]any{"tier": t.Tier, "launcher": t.Launcher, "model": t.Model, "effort": t.Effort}
	if err := s.applyStartTier(ctx, p); err != nil {
		return err
	}
	t.Launcher, _ = p["launcher"].(string)
	t.Model, _ = p["model"].(string)
	t.Effort, _ = p["effort"].(string)
	return nil
}

// modelProfileContext is the snapshot's model profile line: read outside the ordinary source cache so every turn
// observes a switch, including a rollback to no profile, and a tier edited in the profile in use.
func (s *Service) modelProfileContext(ctx context.Context) *prompts.SnapshotProfile {
	s.models.mu.Lock() // the profiles and the state of one switch, not one from either side of it
	p, active, err := s.activeModelProfile(ctx)
	s.models.mu.Unlock()
	switch {
	case err != nil:
		return &prompts.SnapshotProfile{Unreadable: true}
	case p == nil && active != "":
		return &prompts.SnapshotProfile{ID: active, Gone: true}
	case p == nil:
		return nil
	}
	out := &prompts.SnapshotProfile{ID: p.ID, Name: p.Name}
	for _, name := range slices.Sorted(maps.Keys(p.Tiers)) {
		t := p.Tiers[name]
		out.Tiers = append(out.Tiers, prompts.SnapshotTier{Name: name, Launcher: t.Launcher, Model: t.Model, Effort: t.Effort, Description: t.Description})
	}
	return out
}

// activeModelProfile is the profile in use and its id, read without making the templates: nil with "" when none is,
// nil with its id when it is gone (a rollback to a profile deleted since).
func (s *Service) activeModelProfile(ctx context.Context) (*ModelProfile, string, error) {
	ps, _, err := s.readModelProfiles(ctx)
	if err != nil {
		return nil, "", err
	}
	st, err := s.readModelProfileState(ctx)
	if err != nil {
		return nil, "", err
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == st.Active })
	if st.Active == "" || i < 0 {
		return nil, st.Active, nil
	}
	return &ps.Profiles[i], st.Active, nil
}
