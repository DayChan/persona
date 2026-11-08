package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/apierr"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// Model profiles (docs/assistant/model-profiles.md): a named set of model choices the user switches between in one
// step — "performance" while the strong models' quota lasts, "economy" when it runs short. A profile holds each
// category's choice (the model configuration's records, models.go), the model fields of providers added in Persona
// (launchers.managed, models_launchers.go) and named tiers an Agent can be started on (agent_start, task_plan). A
// switch writes all of it in one transaction and keeps what it replaced, so one rollback undoes it; every switch,
// rollback and edit is in the history. Profiles are the user's to edit: the two built-in ones start as copies of the
// configuration they were made from.

const (
	modelProfilesKey     = "model_profiles"       // modelProfiles: the profiles
	modelProfileStateKey = "model_profiles.state" // modelProfileState: which one is in use, the rollback, the history

	maxModelProfiles    = 20
	maxModelTiers       = 12
	maxProfileEvents    = 50
	modelProfileName    = 40  // runes
	modelProfileAbout   = 300 // runes
	modelProfileValue   = 200 // bytes of a provider, model or launcher name
	shownProfileEvents  = 20
	modelProfileRolling = "rollback" // not an id: POST /api/v1/model-profiles/rollback
)

var (
	modelProfileID = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	modelTierName  = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,23}$`)
)

// modelProfileFields are the model fields of a managed launcher a profile can set, by their JSON names.
var modelProfileFields = []string{"model", "small_model", "subagent_model", "opus_model", "sonnet_model", "haiku_model", "fable_model", "effort"}

// ModelProfile is one profile: what it sets, and nothing else (a category or field it leaves out stays as it is).
type ModelProfile struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Purposes: a category's choice (its record in the model configuration) by category id; an empty binding is
	// "follow the default". The main chat's runtime is not a profile's.
	Purposes map[string]ModelBinding `json:"purposes,omitempty"`
	// Launchers: model fields of providers added in Persona, by provider name.
	Launchers map[string]LauncherModels `json:"launchers,omitempty"`
	// Tiers: what an Agent started on a tier runs on (agent_start's and task_plan's tier).
	Tiers     map[string]ModelTier `json:"tiers,omitempty"`
	UpdatedAt time.Time            `json:"updated_at"`
}

// LauncherModels are the model fields of a managed launcher a profile sets: nil leaves a field as it is, "" clears it
// (the launcher's own default then applies; the main model cannot be cleared).
type LauncherModels struct {
	Model         *string `json:"model,omitempty"`
	SmallModel    *string `json:"small_model,omitempty"`
	SubagentModel *string `json:"subagent_model,omitempty"`
	OpusModel     *string `json:"opus_model,omitempty"`
	SonnetModel   *string `json:"sonnet_model,omitempty"`
	HaikuModel    *string `json:"haiku_model,omitempty"`
	FableModel    *string `json:"fable_model,omitempty"`
	Effort        *string `json:"effort,omitempty"`
}

// fields pairs each of modelProfileFields with its value in m and the field of l it sets (nil: only m's).
func (m *LauncherModels) fields(l *proto.ManagedLauncher) []launcherField {
	if l == nil {
		l = &proto.ManagedLauncher{}
	}
	return []launcherField{{"model", &m.Model, &l.Model}, {"small_model", &m.SmallModel, &l.SmallModel},
		{"subagent_model", &m.SubagentModel, &l.SubagentModel}, {"opus_model", &m.OpusModel, &l.OpusModel},
		{"sonnet_model", &m.SonnetModel, &l.SonnetModel}, {"haiku_model", &m.HaikuModel, &l.HaikuModel},
		{"fable_model", &m.FableModel, &l.FableModel}, {"effort", &m.Effort, &l.Effort}}
}

type launcherField struct {
	name     string
	set      **string // the profile's value
	launcher *string  // the launcher's
}

func profileValue(v string) *string { return &v }

// ModelTier is what an Agent started on a tier runs on: a launcher, and its model and effort ("" for its defaults).
type ModelTier struct {
	Launcher string `json:"launcher"`
	Model    string `json:"model,omitempty"`
	Effort   string `json:"effort,omitempty"`
}

// ProfileActor is who switched or edited a profile: a device (its name, from the hub), the hub's own token, or the
// assistant (a conversation's tool call).
type ProfileActor struct {
	Kind string `json:"kind"` // device, hub, assistant
	Name string `json:"name,omitempty"`
}

// ModelProfileEvent is one entry of the history.
type ModelProfileEvent struct {
	At      time.Time    `json:"at"`
	Action  string       `json:"action"` // switch, rollback, save, delete
	From    string       `json:"from,omitempty"`
	To      string       `json:"to,omitempty"` // switch, rollback: the profile now in use; save, delete: the profile
	By      ProfileActor `json:"by"`
	Changed []string     `json:"changed,omitempty"` // what a switch or rollback changed: categories, <launcher>.<field>
}

type modelProfiles struct {
	Revision int64          `json:"revision"`
	Profiles []ModelProfile `json:"profiles"`
}

type modelProfileState struct {
	Active   string              `json:"active,omitempty"` // the profile last switched to ("" none yet)
	At       time.Time           `json:"at,omitzero"`
	By       ProfileActor        `json:"by,omitzero"`
	Previous *modelProfileImage  `json:"previous,omitempty"` // what the last switch replaced: one rollback's worth
	Events   []ModelProfileEvent `json:"events,omitempty"`   // oldest first, the last maxProfileEvents
}

// modelProfileImage is what a switch replaced: the categories' and launcher fields' values before it, and the profile
// that was in use.
type modelProfileImage struct {
	Profile   string                    `json:"profile,omitempty"`
	At        time.Time                 `json:"at"`
	Purposes  map[string]ModelBinding   `json:"purposes,omitempty"`
	Launchers map[string]LauncherModels `json:"launchers,omitempty"`
}

func (st *modelProfileState) record(e ModelProfileEvent) {
	st.Events = append(st.Events, e)
	if n := len(st.Events) - maxProfileEvents; n > 0 {
		st.Events = slices.Delete(st.Events, 0, n)
	}
}

var (
	errModelProfileNotFound    = i18n.Err("assistant-model_profiles.err_not_found")
	errModelProfileConflict    = i18n.Err("assistant-model_profiles.err_conflict")
	errModelProfileNoRollback  = i18n.Err("assistant-model_profiles.err_no_rollback")
	errModelProfileAtomicStore = i18n.Err("assistant-model_profiles.err_atomic_store")
)

// ---- what is stored ----

func (s *Service) readModelProfiles(ctx context.Context) (modelProfiles, bool, error) {
	var ps modelProfiles
	st := s.store()
	if st == nil {
		return ps, false, i18n.Err("assistant-models.err_no_database")
	}
	ok, err := st.Setting(ctx, modelProfilesKey, &ps)
	return ps, ok, err
}

func (s *Service) readModelProfileState(ctx context.Context) (modelProfileState, error) {
	var state modelProfileState
	st := s.store()
	if st == nil {
		return state, i18n.Err("assistant-models.err_no_database")
	}
	_, err := st.Setting(ctx, modelProfileStateKey, &state)
	return state, err
}

// putModelProfileSettings requires an atomic store: a failed write must leave every setting untouched.
func (s *Service) putModelProfileSettings(ctx context.Context, values map[string]any) error {
	st := s.store()
	if st == nil {
		return i18n.Err("assistant-models.err_no_database")
	}
	if tx, ok := st.(interface {
		PutSettings(context.Context, map[string]any) error
	}); ok {
		return tx.PutSettings(ctx, values)
	}
	return errModelProfileAtomicStore
}

// modelProfilesLocked reads profiles or builds two templates from the current configuration (models.mu held).
// The caller persists templates only after its operation has passed all checks.
func (s *Service) modelProfilesLocked(ctx context.Context) (modelProfiles, bool, error) {
	ps, ok, err := s.readModelProfiles(ctx)
	if err != nil || ok {
		return ps, false, err
	}
	purposes, launchers, tiers, err := s.captureModels(ctx)
	if err != nil {
		return ps, false, err
	}
	l, now := s.requestCtxLang(ctx), time.Now().UTC()
	for _, id := range []string{"performance", "economy"} {
		ps.Profiles = append(ps.Profiles, ModelProfile{ID: id, Name: i18n.T(l, "assistant-model_profiles.name_"+id),
			Description: i18n.T(l, "assistant-model_profiles.about_"+id), Purposes: maps.Clone(purposes),
			Launchers: maps.Clone(launchers), Tiers: maps.Clone(tiers), UpdatedAt: now})
	}
	ps.Revision = 1
	return ps, true, nil
}

// captureModels is the configuration as a profile would set it: every category's own choice, the model fields of the
// providers added in Persona those categories run on, and two tiers (strong: the main chat's, standard: the Agents').
func (s *Service) captureModels(ctx context.Context) (map[string]ModelBinding, map[string]LauncherModels, map[string]ModelTier, error) {
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return nil, nil, nil, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	purposes := map[string]ModelBinding{}
	used := map[string]bool{}
	tiers := map[string]ModelTier{}
	for _, id := range modelCategoryIDs {
		r, err := s.modelRecord(ctx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		b := r.Binding
		b.Runtime = ""
		purposes[id] = b
		effective, err := s.resolveModel(ctx, id)
		if err != nil {
			return nil, nil, nil, err
		}
		name, ok := strings.CutPrefix(effective.Provider, "launcher.")
		if !ok {
			continue
		}
		used[name] = true
		tier := map[string]string{"assistant": "strong", "agent.new": "standard"}[id]
		if tier != "" {
			tiers[tier] = ModelTier{Launcher: name, Model: effective.Model, Effort: effective.Effort}
		}
	}
	launchers := map[string]LauncherModels{}
	for _, l := range stored {
		if !used[l.Name] {
			continue
		}
		var m LauncherModels
		for _, f := range m.fields(&l.ManagedLauncher) {
			if l.Kind == "api" && f.name != "model" {
				continue
			}
			*f.set = profileValue(*f.launcher)
		}
		launchers[l.Name] = m
	}
	return purposes, launchers, tiers, nil
}

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
	if len(p.Tiers) > maxModelTiers {
		return i18n.Err("assistant-model_profiles.err_tiers", i18n.Args{"max": maxModelTiers})
	}
	for name, t := range p.Tiers {
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

func profileEffortErr(where, effort string) error {
	return i18n.Err("assistant-model_profiles.err_effort", i18n.Args{"where": where, "effort": effort, "levels": strings.Join(proto.EffortLevels, " / ")})
}

// ---- comparing with the configuration ----

// modelProfileChanges is what switching to p would change now: the categories whose choice differs (ids) and the
// launcher fields (<launcher>.<field>). For the profile in use: where the configuration has moved away from it.
func (s *Service) modelProfileChanges(ctx context.Context, p ModelProfile, stored []storedLauncher) ([]string, error) {
	changes := []string{}
	for _, id := range modelCategoryIDs {
		b, ok := p.Purposes[id]
		if !ok {
			continue
		}
		r, err := s.modelRecord(ctx, id)
		if err != nil {
			return nil, err
		}
		r.Binding.Runtime = ""
		if r.Binding != b {
			changes = append(changes, id)
		}
	}
	for _, name := range slices.Sorted(maps.Keys(p.Launchers)) {
		i := slices.IndexFunc(stored, func(l storedLauncher) bool { return l.Name == name })
		var l *proto.ManagedLauncher
		if i >= 0 {
			l = &stored[i].ManagedLauncher
		}
		m := p.Launchers[name]
		for _, f := range m.fields(l) {
			if *f.set != nil && (l == nil || *f.launcher != **f.set) { // a launcher no longer there: all of it
				changes = append(changes, name+"."+f.name)
			}
		}
	}
	return changes, nil
}

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
		return sw, false, nil // already in use, nothing to do: the rollback stays the one of the switch that got here
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
	s.event(ctx, map[string]any{"type": "model_profile", "active": sw.To})
}

// ---- editing ----

// saveModelProfile changes (or makes) profile id with edit, at the profiles' revision when one is given; the history
// says who. Saving the profile in use does not switch to it again: its changes run after the next switch.
func (s *Service) saveModelProfile(ctx context.Context, revision *int64, id string, by ProfileActor, edit func(p *ModelProfile, exists bool) error) (ModelProfile, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	ps, _, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return ModelProfile{}, err
	}
	if revision != nil && *revision != ps.Revision {
		return ModelProfile{}, errModelProfileConflict
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	p := ModelProfile{ID: id}
	if i >= 0 {
		p = ps.Profiles[i]
		// the values are never written through: a copy of the maps is a copy
		p.Purposes, p.Launchers, p.Tiers = maps.Clone(p.Purposes), maps.Clone(p.Launchers), maps.Clone(p.Tiers)
	} else if len(ps.Profiles) >= maxModelProfiles {
		return p, i18n.Err("assistant-model_profiles.err_profiles", i18n.Args{"max": maxModelProfiles})
	}
	if err := edit(&p, i >= 0); err != nil {
		return p, err
	}
	p.ID = id
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return p, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	if err := checkModelProfile(&p, stored); err != nil {
		return p, err
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
		return p, err
	}
	state.record(ModelProfileEvent{At: now, Action: "save", To: id, By: by})
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return p, err
	}
	s.ProfileChanged(ctx)
	return p, nil
}

// deleteModelProfile removes profile id; not the one in use.
func (s *Service) deleteModelProfile(ctx context.Context, revision *int64, id string, by ProfileActor) error {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	ps, _, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return err
	}
	if revision != nil && *revision != ps.Revision {
		return errModelProfileConflict
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == id })
	if i < 0 {
		return errModelProfileNotFound
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return err
	}
	if state.Active == id {
		return i18n.Err("assistant-model_profiles.err_active", i18n.Args{"name": ps.Profiles[i].Name})
	}
	ps.Profiles = slices.Delete(ps.Profiles, i, i+1)
	ps.Revision++
	state.record(ModelProfileEvent{At: time.Now().UTC(), Action: "delete", To: id, By: by})
	if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps, modelProfileStateKey: state}); err != nil {
		return err
	}
	s.ProfileChanged(ctx)
	return nil
}

// ---- tiers ----

// tierModel is what an Agent started on tier runs on: the tier in the profile in use.
func (s *Service) tierModel(ctx context.Context, tier string) (ModelTier, error) {
	ps, _, err := s.readModelProfiles(ctx)
	if err != nil {
		return ModelTier{}, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return ModelTier{}, err
	}
	i := slices.IndexFunc(ps.Profiles, func(p ModelProfile) bool { return p.ID == state.Active })
	if state.Active == "" || i < 0 {
		return ModelTier{}, errors.New("no model profile is in use: switch to one with model_profile_switch, or leave tier out")
	}
	p := ps.Profiles[i]
	t, ok := p.Tiers[tier]
	if !ok {
		return ModelTier{}, errors.New("the model profile in use (" + p.Name + ") has no tier " + strings.TrimSpace(tier) + "; its tiers: " + strings.Join(slices.Sorted(maps.Keys(p.Tiers)), ", "))
	}
	return t, nil
}

// withTier fills a start's launcher, model and effort from tier: what the start names itself wins, and the tier's
// model and effort go only with the tier's launcher.
func withTier(t ModelTier, launcher, model, effort *string) {
	if *launcher != "" && *launcher != t.Launcher {
		return
	}
	*launcher = t.Launcher
	if *model == "" {
		*model = t.Model
	}
	if *effort == "" {
		*effort = t.Effort
	}
}

// ---- the view ----

// ModelProfileView is a profile as GET /api/v1/model-profiles shows it.
type ModelProfileView struct {
	ModelProfile
	Active bool `json:"active,omitempty"`
	// Changes: what switching to it would change now (for the one in use: where the configuration moved away from it).
	Changes []string `json:"changes"`
}

// ModelProfilesView is GET /api/v1/model-profiles.
type ModelProfilesView struct {
	Revision int64               `json:"revision"`
	Active   string              `json:"active,omitempty"`
	At       *time.Time          `json:"at,omitempty"`
	By       *ProfileActor       `json:"by,omitempty"`
	Rollback *ModelProfileUndo   `json:"rollback,omitempty"`
	Profiles []ModelProfileView  `json:"profiles"`
	Events   []ModelProfileEvent `json:"events"` // newest first
}

// ModelProfileUndo is what a rollback would do: back to profile To ("" none), changing Changes.
type ModelProfileUndo struct {
	To      string    `json:"to,omitempty"`
	At      time.Time `json:"at"`
	Changes []string  `json:"changes"`
}

func (s *Service) modelProfilesView(ctx context.Context) (ModelProfilesView, error) {
	s.models.mu.Lock()
	defer s.models.mu.Unlock()
	ps, seeded, err := s.modelProfilesLocked(ctx)
	if err != nil {
		return ModelProfilesView{}, err
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil {
		return ModelProfilesView{}, err
	}
	stored, err := s.readStoredLaunchers(ctx)
	if err != nil {
		return ModelProfilesView{}, err
	}
	ctx = withManagedSnapshot(ctx, stored)
	v := ModelProfilesView{Revision: ps.Revision, Active: state.Active, Profiles: []ModelProfileView{}, Events: []ModelProfileEvent{}}
	if state.Active != "" {
		at, by := state.At, state.By
		v.At, v.By = &at, &by
	}
	for _, p := range ps.Profiles {
		changes, err := s.modelProfileChanges(ctx, p, stored)
		if err != nil {
			return ModelProfilesView{}, err
		}
		v.Profiles = append(v.Profiles, ModelProfileView{ModelProfile: p, Active: p.ID == state.Active, Changes: changes})
	}
	if prev := state.Previous; prev != nil {
		undo := &ModelProfileUndo{To: prev.Profile, At: prev.At, Changes: slices.Sorted(maps.Keys(prev.Purposes))}
		for _, name := range slices.Sorted(maps.Keys(prev.Launchers)) {
			m := prev.Launchers[name]
			for _, f := range m.fields(nil) {
				if *f.set != nil {
					undo.Changes = append(undo.Changes, name+"."+f.name)
				}
			}
		}
		v.Rollback = undo
	}
	for i := len(state.Events) - 1; i >= 0 && len(v.Events) < shownProfileEvents; i-- {
		v.Events = append(v.Events, state.Events[i])
	}
	if seeded {
		if err := s.putModelProfileSettings(ctx, map[string]any{modelProfilesKey: ps}); err != nil {
			return ModelProfilesView{}, err
		}
	}
	return v, nil
}

// ---- the routes ----

// profileActor is who sent r: the device the hub names, else the hub's own token.
func profileActor(r *http.Request) ProfileActor {
	if name := strings.TrimSpace(r.Header.Get(assistantwire.DeviceNameHeader)); name != "" {
		return ProfileActor{Kind: "device", Name: name}
	}
	return ProfileActor{Kind: "hub"}
}

// modelProfileStatus is the HTTP status of err from the profiles' functions.
func modelProfileStatus(err error) int {
	var m *i18n.Message
	switch {
	case errors.Is(err, errModelProfileAtomicStore):
		return http.StatusInternalServerError
	case errors.Is(err, errModelProfileNotFound):
		return http.StatusNotFound
	case errors.Is(err, errModelProfileConflict), errors.Is(err, errModelProfileNoRollback):
		return http.StatusConflict
	case errors.As(err, &m) && strings.HasPrefix(m.Key, "assistant-model_profiles."):
		return http.StatusBadRequest
	}
	return http.StatusInternalServerError
}

// modelProfileInput is PUT /api/v1/model-profiles/{id}: the whole profile, or (capture) the configuration as it is now
// with the tiers kept.
type modelProfileInput struct {
	Revision    *int64                    `json:"revision"`
	Name        string                    `json:"name"`
	Description string                    `json:"description"`
	Purposes    map[string]ModelBinding   `json:"purposes"`
	Launchers   map[string]LauncherModels `json:"launchers"`
	Tiers       map[string]ModelTier      `json:"tiers"`
	Capture     bool                      `json:"capture"`
}

func (s *Service) modelProfileRoutes() {
	view := func(w http.ResponseWriter, r *http.Request) {
		v, err := s.modelProfilesView(r.Context())
		if err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		apierr.WriteJSON(w, v)
	}
	full := func(fn http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if !fullRole(r) {
				apierr.Write(w, http.StatusForbidden, i18n.Err("assistant-model_profiles.err_role"))
				return
			}
			fn(w, r)
		}
	}
	s.handle("GET /api/v1/model-profiles", view)
	s.handle("PUT /api/v1/model-profiles/{id}", full(func(w http.ResponseWriter, r *http.Request) {
		var in modelProfileInput
		dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
		dec.DisallowUnknownFields()
		if err := dec.Decode(&in); err != nil {
			writeBodyErr(w, err, err)
			return
		}
		if in.Revision == nil || *in.Revision < 0 {
			apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models.err_revision_required"))
			return
		}
		ctx := r.Context()
		_, err := s.saveModelProfile(ctx, in.Revision, r.PathValue("id"), profileActor(r), func(p *ModelProfile, exists bool) error {
			p.Name, p.Description = in.Name, in.Description
			if !in.Capture {
				p.Purposes, p.Launchers, p.Tiers = in.Purposes, in.Launchers, in.Tiers
				return nil
			}
			purposes, launchers, tiers, err := s.captureModels(ctx)
			if err != nil {
				return err
			}
			p.Purposes, p.Launchers = purposes, launchers
			if in.Tiers != nil {
				p.Tiers = in.Tiers
			} else if !exists {
				p.Tiers = tiers
			}
			return nil
		})
		if err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("DELETE /api/v1/model-profiles/{id}", full(func(w http.ResponseWriter, r *http.Request) {
		var revision *int64
		if q := r.URL.Query().Get("revision"); q != "" {
			var n int64
			if err := json.Unmarshal([]byte(q), &n); err != nil {
				apierr.Write(w, http.StatusBadRequest, i18n.Err("assistant-models.err_revision_required"))
				return
			}
			revision = &n
		}
		if err := s.deleteModelProfile(r.Context(), revision, r.PathValue("id"), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("POST /api/v1/model-profiles/{id}/apply", full(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.applyModelProfile(r.Context(), r.PathValue("id"), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
	s.handle("POST /api/v1/model-profiles/rollback", full(func(w http.ResponseWriter, r *http.Request) {
		if _, err := s.rollbackModelProfile(r.Context(), profileActor(r)); err != nil {
			apierr.Write(w, modelProfileStatus(err), err)
			return
		}
		view(w, r)
	}))
}
