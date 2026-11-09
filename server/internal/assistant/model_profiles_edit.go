package assistant

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// ---- checking a profile ----

func validProfileValue(v string) bool {
	return v == strings.TrimSpace(v) && !strings.ContainsAny(v, "\r\n\t\x00") && len(v) <= modelProfileValue
}

// checkModelProfile is what saving p needs: its id, name and the form of what it sets. Whether the providers and
// models can run is checked when it is switched to (applyModelProfile), against the configuration of that moment.
func checkModelProfile(p *ModelProfile, stored []storedLauncher) error {
	p.Name, p.Description = strings.TrimSpace(p.Name), strings.TrimSpace(p.Description)
	if !modelProfileID.MatchString(p.ID) || p.ID == modelProfileRolling {
		return i18n.Err("assistant-model_profiles.err_id")
	}
	if p.Name == "" || utf8.RuneCountInString(p.Name) > modelProfileName || strings.ContainsAny(p.Name, "\r\n\x00") {
		return i18n.Err("assistant-model_profiles.err_name", i18n.Args{"max": modelProfileName})
	}
	if utf8.RuneCountInString(p.Description) > modelProfileAbout || strings.ContainsRune(p.Description, 0) {
		return i18n.Err("assistant-model_profiles.err_description", i18n.Args{"max": modelProfileAbout})
	}
	for id, b := range p.Purposes {
		if !slices.Contains(modelCategoryIDs, id) {
			return i18n.Err("assistant-model_profiles.err_purpose", i18n.Args{"purpose": id, "purposes": strings.Join(modelCategoryIDs, ", ")})
		}
		if b.Runtime != "" {
			return i18n.Err("assistant-model_profiles.err_runtime")
		}
		for field, v := range map[string]string{"provider": b.Provider, "model": b.Model} {
			if !validProfileValue(v) {
				return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": id, "field": field})
			}
		}
		if b.Effort != "" && !slices.Contains(proto.EffortLevels, b.Effort) {
			return profileEffortErr(id, b.Effort)
		}
	}
	for name, m := range p.Launchers {
		i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		if i < 0 {
			return i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
		empty := true
		for _, f := range m.fields(nil) {
			v := *f.set
			if v == nil {
				continue
			}
			empty = false
			if !validProfileValue(*v) {
				return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": name, "field": f.name})
			}
			if f.name == "model" && *v == "" {
				return i18n.Err("assistant-model_profiles.err_launcher_model", i18n.Args{"launcher": name})
			}
			if f.name == "effort" && *v != "" && !slices.Contains(proto.EffortLevels, *v) {
				return profileEffortErr(name, *v)
			}
			if stored[i].Kind == "api" && f.name != "model" && *v != "" {
				return i18n.Err("assistant-model_profiles.err_launcher_api", i18n.Args{"launcher": name, "field": f.name})
			}
		}
		if empty {
			delete(p.Launchers, name)
		}
	}
	if err := checkModelTiers(p.Tiers); err != nil {
		return err
	}
	if len(p.Purposes) == 0 {
		p.Purposes = nil
	}
	if len(p.Launchers) == 0 {
		p.Launchers = nil
	}
	if len(p.Tiers) == 0 {
		p.Tiers = nil
	}
	return nil
}

// checkModelTiers is what saving tiers needs (normalized in place): names, a launcher each, model ids, effort levels
// and one-line descriptions. Whether a launcher exists is the editor's to check (settings_tiers.go): a ~/.zshrc one
// is known only to its computers.
func checkModelTiers(tiers map[string]ModelTier) error {
	if len(tiers) > maxModelTiers {
		return i18n.Err("assistant-model_profiles.err_tiers", i18n.Args{"max": maxModelTiers})
	}
	for name, t := range tiers {
		if !modelTierName.MatchString(name) {
			return i18n.Err("assistant-model_profiles.err_tier_name", i18n.Args{"tier": name})
		}
		if t.Launcher == "" {
			return i18n.Err("assistant-model_profiles.err_tier_launcher", i18n.Args{"tier": name})
		}
		for field, v := range map[string]string{"launcher": t.Launcher, "model": t.Model} {
			if !validProfileValue(v) || strings.Contains(v, " ") {
				return i18n.Err("assistant-model_profiles.err_value", i18n.Args{"where": name, "field": field})
			}
		}
		if t.Effort != "" && !slices.Contains(proto.EffortLevels, t.Effort) {
			return profileEffortErr(name, t.Effort)
		}
		if t.Description = strings.TrimSpace(t.Description); utf8.RuneCountInString(t.Description) > modelTierAbout ||
			strings.ContainsAny(t.Description, "\r\n\x00") {
			return i18n.Err("assistant-model_profiles.err_tier_description", i18n.Args{"tier": name, "max": modelTierAbout})
		}
		tiers[name] = t
	}
	return nil
}

// checkTierModel is what a tier added or changed needs of its model: it goes to the agent as --model, so one that is no
// model id would fail every start on the tier (#1644). Tiers saved before this was checked are left as they are: a
// switch or an edit of something else never stops on them (a start on one says why).
func checkTierModel(name string, t ModelTier) error {
	if t.Model != "" && !proto.ValidModelID(t.Model) {
		return i18n.Err("assistant-model_profiles.err_tier_model", i18n.Args{"tier": name, "model": t.Model})
	}
	return nil
}

func profileEffortErr(where, effort string) error {
	return i18n.Err("assistant-model_profiles.err_effort", i18n.Args{"where": where, "effort": effort, "levels": strings.Join(proto.EffortLevels, " / ")})
}

// ---- editing ----

// saveModelProfile changes (or makes) profile id with edit, at the profiles' revision when one is given; the history
// says who. Saving the profile in use does not switch to it again: its changes run after the next switch (its tiers
// at the next start on one, as they are read then).
func (s *Service) saveModelProfile(ctx context.Context, revision *int64, id string, by ProfileActor, edit func(p *ModelProfile, exists bool) error) (ModelProfile, error) {
	s.models.mu.Lock()
	p, active, err := s.saveModelProfileLocked(ctx, revision, id, by, edit)
	s.models.mu.Unlock()
	if err == nil {
		s.profileEvent(ctx, active)
	}
	return p, err
}

func (s *Service) saveModelProfileLocked(ctx context.Context, revision *int64, id string, by ProfileActor, edit func(p *ModelProfile, exists bool) error) (ModelProfile, string, error) {
	ps, seeded, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return ModelProfile{}, "", err
	}
	if revision != nil && *revision != ps.Revision {
		return ModelProfile{}, "", errModelProfileConflict
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	p := ModelProfile{ID: id}
	var saved map[string]ModelTier // the tiers as they are: only the ones added or changed are held to checkTierModel
	if i >= 0 {
		p, saved = ps.Profiles[i], ps.Profiles[i].Tiers
		// the values are never written through: a copy of the maps is a copy
		p.Purposes, p.Launchers, p.Tiers = maps.Clone(p.Purposes), maps.Clone(p.Launchers), maps.Clone(p.Tiers)
	} else if len(ps.Profiles) >= maxModelProfiles {
		return p, "", i18n.Err("assistant-model_profiles.err_profiles", i18n.Args{"max": maxModelProfiles})
	}
	if err := edit(&p, i >= 0); err != nil {
		return p, "", err
	}
	p.ID = id
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return p, "", err
	}
	ctx = withManagedSnapshot(ctx, stored)
	if err := checkModelProfile(&p, stored); err != nil {
		return p, "", err
	}
	for name, t := range p.Tiers {
		if was, ok := saved[name]; !ok || was.Model != t.Model {
			if err := checkTierModel(name, t); err != nil {
				return p, "", err
			}
		}
	}
	now := time.Now().UTC()
	p.UpdatedAt = now
	if i >= 0 {
		ps.Profiles[i] = p
	} else {
		ps.Profiles = append(ps.Profiles, p)
	}
	ps.Revision++
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return p, "", err
	}
	if seeded {
		state.seed()
	}
	state.record(ModelProfileEvent{At: now, Action: "save", To: id, By: by})
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return p, "", err
	}
	s.ProfileChanged(ctx)
	return p, state.Active, nil
}

// deleteModelProfile removes profile id; not the one in use.
func (s *Service) deleteModelProfile(ctx context.Context, revision *int64, id string, by ProfileActor) error {
	s.models.mu.Lock()
	active, err := s.deleteModelProfileLocked(ctx, revision, id, by)
	s.models.mu.Unlock()
	if err == nil {
		s.profileEvent(ctx, active)
	}
	return err
}

func (s *Service) deleteModelProfileLocked(ctx context.Context, revision *int64, id string, by ProfileActor) (string, error) {
	ps, seeded, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return "", err
	}
	if revision != nil && *revision != ps.Revision {
		return "", errModelProfileConflict
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if i < 0 {
		return "", errModelProfileNotFound
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return "", err
	}
	if seeded {
		state.seed()
	}
	if state.Active == id {
		return "", i18n.Err("assistant-model_profiles.err_active", i18n.Args{"name": ps.Profiles[i].Name})
	}
	ps.Profiles = slices.Delete(ps.Profiles, i, i+1)
	ps.Revision++
	state.record(ModelProfileEvent{At: time.Now().UTC(), Action: "delete", To: id, By: by})
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return "", err
	}
	s.ProfileChanged(ctx)
	return state.Active, nil
}
