package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"slices"

	"github.com/nwverse/persona/server/internal/i18n"
)

// editable is a plugin the user may switch and configure (not hub.json's).
func (r *Registry) editable(id string) (*Extension, error) {
	p := r.Get(id)
	if p == nil {
		return nil, i18n.Err("extension-status.extension_missing", i18n.Args{"id": id})
	}
	if p.ReadOnly() {
		return nil, i18n.Err("extension-status.read_only", i18n.Args{"id": id})
	}
	if p.Record.Source == SourceDev {
		return nil, i18n.Err("extension-status.dev_read_only", i18n.Args{"id": id})
	}
	return p, nil
}

// SetEnabled switches a plugin on or off; on only when CanEnable lets it.
func (r *Registry) SetEnabled(ctx context.Context, id string, on bool, actor string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	r.mu.Lock()
	held := r.keptOff[id]
	r.mu.Unlock()
	if on && held != nil {
		return held.why
	}
	if on && !p.Record.Enabled {
		if err := r.canEnable(ctx, p.Record.Source, p.Manifest); err != nil {
			return err
		}
	}
	r.mu.Lock()
	row := p.Record
	row.Enabled = on
	r.mu.Unlock()
	if err := r.write(ctx, row); err != nil { // the owner's choice, held off or not
		return err
	}
	r.mu.Lock()
	p.Record.Enabled = on
	if held != nil {
		held.row = on
	}
	r.mu.Unlock()
	kind := map[bool]string{true: "enabled", false: "disabled"}[on]
	unlock()
	r.audit(ctx, id, kind, actor, nil)
	r.changed(Change{ID: id, Kind: kind})
	return nil
}

// SetConfig replaces a plugin's configuration, held to its schema first.
func (r *Registry) SetConfig(ctx context.Context, id string, raw json.RawMessage, actor string) error {
	_, err := r.SetConfigClearing(ctx, id, raw, actor)
	return err
}

// ErrSecretNotCleared: a secret the new configuration unbinds (BoundSecrets) could not be deleted, so the
// configuration was not saved.
var ErrSecretNotCleared = i18n.Err("extension-status.secret_not_cleared")

// SetConfigClearing is SetConfig saying which secrets the change deleted (BoundSecrets): those that had a value, or
// may have had (they could not be read). They go first, fail closed: one that cannot be deleted saves nothing
// (ErrSecretNotCleared); a configuration that then cannot be saved answers its error with the ones already gone.
func (r *Registry) SetConfigClearing(ctx context.Context, id string, raw json.RawMessage, actor string) (cleared []string, err error) {
	checked, err := r.checkSettings(ctx, id, raw, "", "")
	if err != nil {
		return nil, err
	}
	unlock, err := r.beginOperation()
	if err != nil {
		return nil, err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return nil, err
	}
	id = p.Record.ID
	if err := r.stillChecked(p, checked); err != nil {
		return nil, err
	}
	if err := p.Schema.Check(raw); err != nil {
		return nil, err
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, err
	}
	compact, _ := json.Marshal(v)
	cleared, err = r.clearBound(ctx, p, compact)
	if err == nil {
		r.mu.Lock()
		row := p.Record
		row.Config = compact
		r.mu.Unlock()
		err = r.put(ctx, row)
	}
	if err == nil {
		r.mu.Lock()
		p.Record.Config = compact
		r.mu.Unlock()
	}
	unlock()
	for _, name := range cleared {
		r.audit(ctx, id, "secret_delete", actor, map[string]any{"name": name, "why": "config"})
	}
	if err != nil {
		if len(cleared) > 0 { // the program runs again without them, on the configuration it had
			r.changed(Change{ID: id, Kind: "secret"})
		}
		return cleared, err
	}
	r.audit(ctx, id, "config", actor, nil) // not the values: they may be personal
	r.changed(Change{ID: id, Kind: "config"})
	return cleared, nil
}

// clearBound deletes the secrets of p that configuration next unbinds (BoundSecrets), whether or not they are
// known to have a value (a failed read must not keep one), and says which went. Called in an operation.
func (r *Registry) clearBound(ctx context.Context, p *Extension, next json.RawMessage) ([]string, error) {
	if r.BoundSecrets == nil || r.store == nil {
		return nil, nil
	}
	names := r.BoundSecrets(p, r.Config(p), p.Schema.Effective(next))
	if len(names) == 0 {
		return nil, nil
	}
	had, readErr := r.store.ExtensionSecrets(ctx, p.Record.StorageKey()) // only to say which went
	var cleared []string
	for _, name := range names {
		if err := r.store.DeleteExtensionSecret(ctx, p.Record.StorageKey(), name); err != nil {
			return cleared, fmt.Errorf("%w（%s：%v）", ErrSecretNotCleared, name, err)
		}
		if _, ok := had[name]; ok || readErr != nil {
			cleared = append(cleared, name)
		}
	}
	return cleared, nil
}

// Config is what the plugin gets: its configuration with the schema's defaults.
func (r *Registry) Config(p *Extension) json.RawMessage {
	r.mu.Lock()
	raw := p.Record.Config
	r.mu.Unlock()
	return p.Schema.Effective(raw)
}

// SavedConfig is the configuration as the user saved it, without the schema's defaults ({} when none).
func (r *Registry) SavedConfig(p *Extension) json.RawMessage {
	r.mu.Lock()
	raw := slices.Clone(p.Record.Config)
	r.mu.Unlock()
	if len(raw) == 0 {
		return json.RawMessage(`{}`)
	}
	return raw
}

func (r *Registry) secretSpec(p *Extension, name string) error {
	if !slices.ContainsFunc(p.Manifest.Secrets, func(s SecretSpec) bool { return s.Name == name }) {
		return i18n.Err("extension-status.secret_missing", i18n.Args{"id": p.Record.ID, "name": name})
	}
	return nil
}

// SetSecret keeps one of a plugin's declared secrets, sealed. The value never goes to a log or back to anyone.
func (r *Registry) SetSecret(ctx context.Context, id, name, value, actor string) error {
	checked, err := r.checkSettings(ctx, id, nil, name, value)
	if err != nil {
		return err
	}
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	if err := r.secretSpec(p, name); err != nil {
		return err
	}
	if err := r.stillChecked(p, checked); err != nil {
		return err
	}
	if r.SecretNeeds != nil {
		if err := r.SecretNeeds(p, name, r.Config(p)); err != nil {
			return err
		}
	}
	if value == "" || len(value) > SecretMost {
		return i18n.Err("extension-status.secret_size", i18n.Args{"max": SecretMost >> 10})
	}
	if r.store == nil || r.seal == nil {
		return i18n.Err("extension-status.secret_no_database")
	}
	if err := r.ensureRow(ctx, p); err != nil {
		return err
	}
	sealed, err := r.seal.Seal(value)
	if err != nil {
		return err
	}
	if err := r.store.PutExtensionSecret(ctx, p.Record.StorageKey(), name, sealed); err != nil {
		return err
	}
	unlock()
	r.audit(ctx, id, "secret_set", actor, map[string]any{"name": name})
	r.changed(Change{ID: id, Kind: "secret"})
	return nil
}

// DeleteSecret forgets one of a plugin's secrets.
func (r *Registry) DeleteSecret(ctx context.Context, id, name, actor string) error {
	unlock, err := r.beginOperation()
	if err != nil {
		return err
	}
	defer unlock()
	p, err := r.editable(id)
	if err != nil {
		return err
	}
	id = p.Record.ID
	if err := r.secretSpec(p, name); err != nil {
		return err
	}
	if r.store == nil {
		return nil
	}
	if err := r.store.DeleteExtensionSecret(ctx, p.Record.StorageKey(), name); err != nil {
		return err
	}
	unlock()
	r.audit(ctx, id, "secret_delete", actor, map[string]any{"name": name})
	r.changed(Change{ID: id, Kind: "secret"})
	return nil
}

// ensureRow gives a built-in plugin its row before a secret refers to it.
func (r *Registry) ensureRow(ctx context.Context, p *Extension) error {
	if p.Record.Source != SourceBuiltin {
		return nil
	}
	rows, err := r.store.Extensions(ctx)
	if err != nil {
		return err
	}
	if slices.ContainsFunc(rows, func(x Record) bool { return x.ID == p.Record.ID }) {
		return nil
	}
	r.mu.Lock()
	row := p.Record
	r.mu.Unlock()
	return r.put(ctx, row)
}

// Secrets are a plugin's secrets in plain text, for handing to it only.
func (r *Registry) Secrets(ctx context.Context, id string) (map[string]string, error) {
	r.mu.Lock()
	dev, isDev := r.devSecrets[id]
	r.mu.Unlock()
	if isDev {
		return maps.Clone(dev), nil
	}
	if r.store == nil || r.seal == nil {
		return nil, nil
	}
	sealed, err := r.store.ExtensionSecrets(ctx, r.storageKey(id))
	if err != nil {
		return nil, err
	}
	out := make(map[string]string, len(sealed))
	for name, s := range sealed {
		plain, err := r.seal.Open(s)
		if err != nil {
			return nil, i18n.Err("extension-status.secret_decrypt", i18n.Args{"id": id, "name": name})
		}
		out[name] = plain
	}
	return out, nil
}

// SecretsSet says which declared secrets have a value.
func (r *Registry) SecretsSet(ctx context.Context, p *Extension) map[string]bool {
	out := map[string]bool{}
	if p.Record.Source == SourceDev {
		r.mu.Lock()
		for _, s := range p.Manifest.Secrets {
			_, out[s.Name] = r.devSecrets[p.Record.ID][s.Name]
		}
		r.mu.Unlock()
		return out
	}
	if r.store == nil || p.ReadOnly() {
		return out
	}
	sealed, err := r.store.ExtensionSecrets(ctx, p.Record.StorageKey())
	if err != nil {
		return out
	}
	for _, s := range p.Manifest.Secrets {
		_, out[s.Name] = sealed[s.Name]
	}
	return out
}

// Missing are what a plugin needs before it can run: required secrets without a value, a configuration its schema
// does not take (after an upgrade changed it). Empty: ready.
func (r *Registry) Missing(ctx context.Context, p *Extension) []string {
	if p == nil {
		return nil
	}
	g := r.dependencyGraph(ctx)
	if g.nodes[p.Record.ID] == nil {
		g.nodes[p.Record.ID] = snapshotExtension(p)
	}
	return slices.Clone(g.problems(p.Record.ID))
}

// ErrSettingsMoved: the settings changed while a change of them was being checked (settings_check); nothing saved.
var ErrSettingsMoved = i18n.Err("extension-status.settings_moved")

// settingsChecked is what a change was checked against: the configuration the extension had then (nil: not checked).
type settingsChecked struct{ config json.RawMessage }

// checkSettings has extension id check its settings as a change would leave them (SettingsCheck): config replaced
// by next (nil: kept), or secret name set to value (name ""). Only for one that declares settings_check, and only with
// values that pass the schema and the secret's rules first (those errors are the registry's own, later).
func (r *Registry) checkSettings(ctx context.Context, id string, next json.RawMessage, name, value string) (*settingsChecked, error) {
	p := r.Get(id)
	if p == nil || !p.Manifest.SettingsCheck || r.SettingsCheck == nil {
		return nil, nil
	}
	now := r.Config(p)
	config := now
	if next != nil {
		if p.Schema.Check(next) != nil {
			return nil, nil
		}
		config = p.Schema.Effective(next)
	}
	if name != "" && (r.secretSpec(p, name) != nil || value == "" || len(value) > SecretMost) {
		return nil, nil
	}
	secrets, err := r.Secrets(ctx, id)
	if err != nil {
		return nil, err
	}
	if secrets == nil {
		secrets = map[string]string{}
	}
	if name != "" {
		secrets[name] = value
	}
	if err := r.SettingsCheck(ctx, p, config, secrets); err != nil {
		return nil, err
	}
	return &settingsChecked{config: now}, nil
}

// stillChecked refuses a change whose check (checkSettings) saw other settings than p has now: one saved meanwhile
// was not checked with this one. Called in the operation.
func (r *Registry) stillChecked(p *Extension, c *settingsChecked) error {
	if c == nil || string(r.Config(p)) == string(c.config) {
		return nil
	}
	return ErrSettingsMoved
}
