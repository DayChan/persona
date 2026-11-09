package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// persona_settings model_profiles.tiers (docs/assistant/model-profiles.md "Agent presets"): the tiers of the model
// profiles, read and edited in a conversation, so they can be set up from every app without an editor of their own.
// A set changes one tier in every profile unless it names some, so a switch keeps it. What it would change is checked
// first (a launcher no computer has never reaches the card), shown on the approval card, and saved at the revision the
// card was made from: profiles edited in the meantime are a conflict, never overwritten.

// tierEdit is model_profiles.tiers' set value. A field left out keeps the tier's own; a different launcher drops the
// old model and effort (they were the old launcher's), as a provider change does in Model Settings.
type tierEdit struct {
	Tier        string   `json:"tier"`
	Launcher    *string  `json:"launcher"`
	Model       *string  `json:"model"`
	Effort      *string  `json:"effort"`
	Description *string  `json:"description"`
	Profiles    []string `json:"profiles"`
	Remove      bool     `json:"remove"`
}

// tierChange is what a set does to one profile: the tier before and after (nil: none).
type tierChange struct {
	ID, Name      string
	Before, After *ModelTier
}

// editTiers applies e to ps (fresh from the store; changed in place) and says what changed in which profile. Only the
// tiers are checked: something else in a profile that no longer holds (a deleted provider) does not stop a tier edit.
func editTiers(ps *modelProfiles, e tierEdit, now time.Time) ([]tierChange, error) {
	targets := e.Profiles
	if len(targets) == 0 {
		for _, p := range ps.Profiles {
			targets = append(targets, p.ID)
		}
	}
	var changes []tierChange
	for _, id := range targets {
		i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
		if i < 0 {
			return nil, fmt.Errorf("no model profile %q (profiles: %s)", id, profileIDs(ps.Profiles))
		}
		p := ps.Profiles[i]
		p.Tiers = maps.Clone(p.Tiers)
		was, had := p.Tiers[e.Tier]
		if e.Remove {
			if !had {
				continue
			}
			delete(p.Tiers, e.Tier)
		} else {
			t := was
			if e.Launcher != nil && strings.TrimSpace(*e.Launcher) != t.Launcher {
				t = ModelTier{Launcher: strings.TrimSpace(*e.Launcher), Description: t.Description}
			}
			for _, f := range []struct {
				set  *string
				into *string
			}{{e.Model, &t.Model}, {e.Effort, &t.Effort}, {e.Description, &t.Description}} {
				if f.set != nil {
					*f.into = strings.TrimSpace(*f.set)
				}
			}
			if !had && t.Launcher == "" {
				return nil, fmt.Errorf("%s (%s) has no tier %s yet: give its launcher to add it there, or name other profiles", p.Name, p.ID, e.Tier)
			}
			if had && t == was {
				continue
			}
			if p.Tiers == nil {
				p.Tiers = map[string]ModelTier{}
			}
			p.Tiers[e.Tier] = t
		}
		if err := checkModelTiers(p.Tiers); err != nil {
			return nil, fmt.Errorf("%s (%s): %w", p.Name, p.ID, err)
		}
		if t, ok := p.Tiers[e.Tier]; ok {
			if err := checkTierModel(e.Tier, t); err != nil { // only the tier edited: another's old model blocks nothing
				return nil, fmt.Errorf("%s (%s): %w", p.Name, p.ID, err)
			}
		}
		if len(p.Tiers) == 0 {
			p.Tiers = nil
		}
		c := tierChange{ID: p.ID, Name: p.Name}
		if had {
			c.Before = &was
		}
		if t, ok := p.Tiers[e.Tier]; ok {
			c.After = &t
		}
		p.UpdatedAt = now
		ps.Profiles[i] = p
		changes = append(changes, c)
	}
	if e.Remove && len(changes) == 0 {
		return nil, fmt.Errorf("no profile named has a tier %s", e.Tier)
	}
	return changes, nil
}

// sameTierChanges: whether two sets of changes are the same, profile by profile.
func sameTierChanges(a, b []tierChange) bool {
	same := func(x, y *ModelTier) bool { return (x == nil) == (y == nil) && (x == nil || *x == *y) }
	return slices.EqualFunc(a, b, func(x, y tierChange) bool {
		return x.ID == y.ID && same(x.Before, y.Before) && same(x.After, y.After)
	})
}

func profileIDs(ps []ModelProfile) string {
	ids := make([]string, 0, len(ps))
	for _, p := range ps {
		ids = append(ids, p.ID)
	}
	return strings.Join(ids, ", ")
}

// tierLauncherCheck says whether launcher can start an Agent somewhere: plain claude, a provider added in Persona that
// runs as a launcher, or one a computer lists (as last seen, and asked again before saying no: one just added to an
// rc file is not refused).
func (s *Service) tierLauncherCheck(ctx context.Context, name string) error {
	if name == "" || name == "claude" {
		return nil // "" is checkModelTiers' to refuse
	}
	known := []string{"claude"}
	for _, l := range s.storedLaunchers(ctx) {
		if l.Name == name {
			if l.Kind == "api" && !messagesAPI(l.API) {
				return i18n.Err("assistant-models.err_api_as_launcher")
			}
			return nil
		}
		if l.Kind != "api" || messagesAPI(l.API) {
			known = append(known, l.Name)
		}
	}
	for _, refresh := range []bool{false, true} {
		for _, m := range s.zshrcLaunchers(ctx, refresh) {
			for _, l := range m.Launchers {
				if l.Name == name {
					return nil
				}
				known = append(known, l.Name)
			}
		}
	}
	slices.Sort(known)
	return fmt.Errorf("no computer has a launcher %q (launchers: %s)", name, strings.Join(slices.Compact(known), ", "))
}

func (s *Service) setModelTiers(ctx context.Context, v json.RawMessage) (*ownChange, error) {
	var e tierEdit
	dec := json.NewDecoder(bytes.NewReader(v))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, fmt.Errorf("value must be an object: %s (%v)", modelTiersValue, err)
	}
	e.Tier = strings.TrimSpace(e.Tier)
	if !modelTierName.MatchString(e.Tier) {
		return nil, i18n.Err("assistant-model_profiles.err_tier_name", i18n.Args{"tier": e.Tier})
	}
	given := e.Launcher != nil || e.Model != nil || e.Effort != nil || e.Description != nil
	switch {
	case e.Remove && given:
		return nil, errors.New("remove takes only tier and profiles")
	case !e.Remove && !given:
		return nil, errors.New("give what to set (launcher, model, effort, description), or remove: true")
	}
	if e.Launcher != nil {
		// before the card, and outside models.mu: a computer may be asked for its launchers
		if err := s.tierLauncherCheck(ctx, strings.TrimSpace(*e.Launcher)); err != nil {
			return nil, err
		}
	}
	s.models.mu.Lock()
	ps, _, err := s.modelProfilesLocked(ctx)
	var changes []tierChange
	if err == nil {
		changes, err = editTiers(&ps, e, time.Now().UTC())
	}
	s.models.mu.Unlock()
	if err != nil {
		return nil, err
	}
	if len(changes) == 0 {
		return &ownChange{summary: "Nothing to change: tier " + e.Tier + " is already like that in every profile named."}, nil
	}
	if s.store() == nil {
		return nil, errors.New("no database on this hub")
	}
	revision := ps.Revision
	return &ownChange{cardText: func(l i18n.Lang) string {
		lines := make([]string, 0, len(changes))
		for _, c := range changes {
			lines = append(lines, i18n.T(l, "assistant-settings_tool.change_model_tier", i18n.Args{"profile": c.Name, "tier": e.Tier,
				"before": tierCardText(l, c.Before), "after": tierCardText(l, c.After)}))
		}
		return strings.Join(lines, "\n")
	},
		apply: func(ctx context.Context) (string, error) { return s.saveTierEdit(ctx, revision, e, changes) },
	}, nil
}

// tierCardText is a tier as the approval card shows it in l.
func tierCardText(l i18n.Lang, t *ModelTier) string {
	if t == nil {
		return i18n.T(l, "assistant-settings_tool.none")
	}
	text := t.runs()
	if t.Description != "" {
		text += " — " + t.Description
	}
	return clipped(l, text)
}

// saveTierEdit makes the edit the user approved, at the revision its card was made from, and only when it still changes
// what the card said: templates never saved are made again from the configuration, at the same revision.
func (s *Service) saveTierEdit(ctx context.Context, revision int64, e tierEdit, card []tierChange) (string, error) {
	s.models.mu.Lock()
	text, active, err := s.saveTierEditLocked(ctx, revision, e, card)
	s.models.mu.Unlock()
	if err == nil {
		s.profileEvent(ctx, active)
	}
	return text, err
}

func (s *Service) saveTierEditLocked(ctx context.Context, revision int64, e tierEdit, card []tierChange) (string, string, error) {
	ps, seeded, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return "", "", err
	}
	stale := errors.New("the model profiles changed after the card was shown, so nothing was saved: set it again for a new card")
	if ps.Revision != revision {
		return "", "", stale
	}
	now := time.Now().UTC()
	changes, err := editTiers(&ps, e, now)
	if err != nil {
		return "", "", err
	}
	if !sameTierChanges(changes, card) {
		return "", "", stale
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return "", "", err
	}
	if seeded {
		state.seed()
	}
	// one event for the edit, however many profiles it changed: <profile>.tiers.<name> each
	event := ModelProfileEvent{At: now, Action: "save", By: ProfileActor{Kind: "assistant"}}
	for _, c := range changes {
		event.Changed = append(event.Changed, c.ID+".tiers."+e.Tier)
	}
	state.record(event)
	ps.Revision++
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return "", "", err
	}
	s.ProfileChanged(ctx)
	var done, without []string
	for _, c := range changes {
		done = append(done, c.ID)
	}
	for _, p := range ps.Profiles {
		if _, ok := p.Tiers[e.Tier]; !ok {
			without = append(without, p.ID)
		}
	}
	verb := "Saved tier "
	if e.Remove {
		verb = "Removed tier "
	}
	text := verb + e.Tier + " in " + strings.Join(done, ", ") + "."
	if len(without) > 0 && !e.Remove {
		text += " Profiles without it (a start on it fails while one of them is in use): " + strings.Join(without, ", ") + "."
	}
	if state.Active == "" {
		text += " No profile is in use: tiers start nothing until one is switched to (model_profile_switch)."
	}
	return text, state.Active, nil
}

// getModelTiers is model_profiles.tiers' get: every profile's tiers, the one in use marked.
func (s *Service) getModelTiers(ctx context.Context) (string, error) {
	v, err := s.modelProfilesView(ctx)
	if err != nil {
		return "", err
	}
	var b strings.Builder
	for _, p := range v.Profiles {
		b.WriteString("- " + p.Name + " (" + p.ID + ")")
		if p.Active {
			b.WriteString(", in use")
		}
		b.WriteByte(':')
		if len(p.Tiers) == 0 {
			b.WriteString(" no tiers")
		}
		for _, name := range slices.Sorted(maps.Keys(p.Tiers)) {
			t := p.Tiers[name]
			b.WriteString("\n  - " + name + " = " + t.runs())
			if t.Description != "" {
				b.WriteString(" — " + t.Description)
			}
		}
		b.WriteByte('\n')
	}
	if v.Active == "" {
		b.WriteString("No profile is in use: tiers start nothing until one is switched to (model_profile_switch).\n")
	}
	b.WriteString("Effort levels: " + strings.Join(proto.EffortLevels, ", ") + ".")
	return b.String(), nil
}

// modelTiersValue is the shape of model_profiles.tiers' set value, as the setting's list entry says it.
const modelTiersValue = `{"tier": a name (lowercase letters, digits, _ -), "launcher": a launcher name, "model": a model id ("" the launcher's default), ` +
	`"effort": a level ("" the launcher's), "description": when to use it (one line, at most 100 characters), ` +
	`"profiles": [profile ids] (default: every profile), "remove": true to delete it}; a field left out keeps the tier's own ` +
	`(a different launcher drops the old model and effort), a new tier needs its launcher`
