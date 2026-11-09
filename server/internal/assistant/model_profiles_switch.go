package assistant

import (
	"context"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

// ---- switching ----

// modelProfileSwitch is what applyModelProfile and rollbackModelProfile did.
type modelProfileSwitch struct {
	From, To string
	Changed  []string
}

// applyModelProfile switches to profile id: every category it sets is checked as a save in Settings › Model
// configuration would check it, then all of it, the launcher fields, what they replace (the rollback) and the history
// are written in one transaction. Nothing is written when anything fails. A switch to the profile already in use
// that changes nothing keeps the rollback as it was.
func (s *Service) applyModelProfile(ctx context.Context, id string, by ProfileActor) (modelProfileSwitch, error) {
	s.models.mu.Lock()
	sw, pushed, err := s.applyModelProfileLocked(ctx, id, by)
	s.models.mu.Unlock()
	if err == nil && (sw.From != sw.To || len(sw.Changed) > 0) {
		s.modelProfileSwitched(ctx, sw, pushed)
	}
	return sw, err
}

func (s *Service) applyModelProfileLocked(ctx context.Context, id string, by ProfileActor) (modelProfileSwitch, bool, error) {
	ps, seeded, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if i < 0 {
		return modelProfileSwitch{}, false, errModelProfileNotFound
	}
	p := ps.Profiles[i]
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	if err := checkModelProfile(&p, stored); err != nil {
		return modelProfileSwitch{}, false, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	if seeded {
		state.seed()
	}
	now := time.Now().UTC()
	image := &modelProfileImage{Profile: state.Active, At: now, Purposes: map[string]ModelBinding{}, Launchers: map[string]LauncherModels{}}
	writes := map[string]any{}
	sw := modelProfileSwitch{From: state.Active, To: id}
	providers := s.modelProviders(ctx)
	for _, cat := range modelCategoryIDs {
		b, ok := p.Purposes[cat]
		if !ok {
			continue
		}
		purpose, err := s.modelPurpose(ctx, cat, providers)
		if err != nil {
			return sw, false, err
		}
		b.Runtime = purpose.Override.Runtime // the main chat's runtime is not the profile's
		if b == purpose.Override {
			continue
		}
		base, _, err := s.modelBaseline(ctx, purpose)
		if err != nil {
			return sw, false, err
		}
		effective := mergeModel(base, b)
		if body, err := s.triageInUse(ctx, cat, effective); err != nil {
			return sw, false, err
		} else if body != nil {
			return sw, false, i18n.Err("assistant-model_profiles.err_cannot_run", i18n.Args{"purpose": purpose.Name, "error": body["error"]})
		}
		if b.Effort != "" && purpose.Kind == "api" {
			config, err := s.apiConfig(ctx, effective)
			if err != nil {
				return sw, false, err
			}
			if config.API != "responses" {
				return sw, false, i18n.Err("assistant-models.err_api_effort")
			}
		}
		modelOnly := b
		modelOnly.Runtime = ""
		if modelOnly != (ModelBinding{}) {
			if err := s.validateModel(ctx, purpose, effective, providers); err != nil {
				return sw, false, i18n.Err("assistant-model_profiles.err_cannot_run", i18n.Args{"purpose": purpose.Name, "error": err})
			}
		}
		image.Purposes[cat] = purpose.Override
		writes[modelRecordKey(cat)] = modelRecord{Revision: purpose.Revision + 1, Binding: b}
		sw.Changed = append(sw.Changed, cat)
	}
	changedLaunchers := false
	for _, name := range slices.Sorted(maps.Keys(p.Launchers)) {
		li := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		if li < 0 {
			return sw, false, i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
		set := p.Launchers[name]
		var before LauncherModels
		was := before.fields(&stored[li].ManagedLauncher)
		for k, f := range set.fields(&stored[li].ManagedLauncher) {
			if *f.set == nil || *f.launcher == **f.set {
				continue
			}
			if stored[li].Kind == "api" && f.name != "model" && **f.set != "" {
				return sw, false, i18n.Err("assistant-model_profiles.err_launcher_api", i18n.Args{"launcher": name, "field": f.name})
			}
			*was[k].set = profileValue(*f.launcher)
			*f.launcher = **f.set
			sw.Changed = append(sw.Changed, name+"."+f.name)
			changedLaunchers = true
		}
		if before != (LauncherModels{}) {
			image.Launchers[name] = before
			stored[li].Updated = now
		}
	}
	if len(sw.Changed) == 0 && state.Active == id {
		// already in use, nothing to do: the rollback stays the one of the switch that got here (or none, just made)
		if seeded {
			return sw, false, s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state})
		}
		return sw, false, nil
	}
	if changedLaunchers {
		writes[launchersManagedKey] = stored
	}
	state.Active, state.At, state.By, state.Previous = id, now, by, image
	state.record(ModelProfileEvent{At: now, Action: "switch", From: sw.From, To: id, By: by, Changed: sw.Changed})
	writes[modelProfileStateKey] = state
	if seeded {
		writes[modelProfilesKey] = ps
	}
	if err := s.putModelProfileSettings(ctx, writes); err != nil {
		return sw, false, err
	}
	return sw, changedLaunchers, nil
}

// rollbackModelProfile undoes the last switch: what it replaced goes back, the profile in use before it is in use
// again. What the rollback replaced is kept in turn, so a second rollback redoes the switch.
func (s *Service) rollbackModelProfile(ctx context.Context, by ProfileActor) (modelProfileSwitch, error) {
	s.models.mu.Lock()
	sw, pushed, err := s.rollbackModelProfileLocked(ctx, by)
	s.models.mu.Unlock()
	if err == nil && (sw.From != sw.To || len(sw.Changed) > 0) {
		s.modelProfileSwitched(ctx, sw, pushed)
	}
	return sw, err
}

func (s *Service) rollbackModelProfileLocked(ctx context.Context, by ProfileActor) (modelProfileSwitch, bool, error) {
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	prev := state.Previous
	if prev == nil {
		return modelProfileSwitch{}, false, errModelProfileNoRollback
	}
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return modelProfileSwitch{}, false, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	for name := range prev.Launchers {
		if !slices.ContainsFunc(stored, func(l storedLauncher) bool { return l.Name == name }) {
			return modelProfileSwitch{}, false, i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
	}
	var providers []ModelProvider
	for _, b := range prev.Purposes {
		name, ok := strings.CutPrefix(b.Provider, "launcher.")
		if !ok {
			continue
		}
		if providers == nil {
			providers = s.modelProviders(ctx)
		}
		if !slices.ContainsFunc(providers, func(p ModelProvider) bool { return p.ID == b.Provider }) {
			return modelProfileSwitch{}, false, i18n.Err("assistant-model_profiles.err_launcher_missing", i18n.Args{"launcher": name})
		}
	}
	now := time.Now().UTC()
	sw := modelProfileSwitch{From: state.Active, To: prev.Profile}
	undo := &modelProfileImage{Profile: state.Active, At: now, Purposes: map[string]ModelBinding{}, Launchers: map[string]LauncherModels{}}
	writes := map[string]any{}
	for _, cat := range modelCategoryIDs {
		b, ok := prev.Purposes[cat]
		if !ok {
			continue
		}
		// Restore model choices while preserving the runtime selected since the switch.
		r, err := s.modelRecord(ctx, cat)
		if err != nil {
			return sw, false, err
		}
		b.Runtime = r.Binding.Runtime
		if b == r.Binding {
			continue
		}
		undo.Purposes[cat] = r.Binding
		writes[modelRecordKey(cat)] = modelRecord{Revision: r.Revision + 1, Binding: b}
		sw.Changed = append(sw.Changed, cat)
	}
	changedLaunchers := false
	for _, name := range slices.Sorted(maps.Keys(prev.Launchers)) {
		li := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		set := prev.Launchers[name]
		var before LauncherModels
		was := before.fields(&stored[li].ManagedLauncher)
		for k, f := range set.fields(&stored[li].ManagedLauncher) {
			if *f.set == nil || *f.launcher == **f.set {
				continue
			}
			*was[k].set = profileValue(*f.launcher)
			*f.launcher = **f.set
			sw.Changed = append(sw.Changed, name+"."+f.name)
			changedLaunchers = true
		}
		if before != (LauncherModels{}) {
			undo.Launchers[name] = before
			stored[li].Updated = now
		}
	}
	if changedLaunchers {
		writes[launchersManagedKey] = stored
	}
	if len(sw.Changed) == 0 && state.Active == prev.Profile {
		return sw, false, nil
	}
	state.Active, state.At, state.By, state.Previous = prev.Profile, now, by, undo
	state.record(ModelProfileEvent{At: now, Action: "rollback", From: sw.From, To: sw.To, By: by, Changed: sw.Changed})
	writes[modelProfileStateKey] = state
	if err := s.putModelProfileSettings(ctx, writes); err != nil {
		return sw, false, err
	}
	return sw, changedLaunchers, nil
}

// modelProfileSwitched tells everyone after a switch or rollback: the nodes get the launchers (when their fields
// changed), the next message to the assistant a new snapshot, the apps an event.
func (s *Service) modelProfileSwitched(ctx context.Context, sw modelProfileSwitch, launchers bool) {
	if launchers {
		s.pushManaged(context.WithoutCancel(ctx), "")
	}
	s.ProfileChanged(ctx)
	s.profileEvent(ctx, sw.To)
}

// profileEvent tells the apps the profiles changed (a switch, a rollback, an edit): they read them again.
func (s *Service) profileEvent(ctx context.Context, active string) {
	s.event(ctx, map[string]any{"type": "model_profile", "active": active})
}
