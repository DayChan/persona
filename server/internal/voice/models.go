package voice

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// ModelChoice and VoiceChoice are what GET /models and GET /voices list (docs/extensions/extensions.md "Voice").
type ModelChoice struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

type VoiceChoice struct {
	ID        string   `json:"id"`
	Name      string   `json:"name,omitempty"`
	Gender    string   `json:"gender,omitempty"`
	Languages []string `json:"languages,omitempty"`
}

type voiceList struct {
	Voices  []VoiceChoice `json:"voices"`
	Default string        `json:"default,omitempty"`
}

// The voices Codex offers gpt-live-1-codex (codex-rs RealtimeVoicesList v3), cove its default.
var codexVoices = []string{"juniper", "maple", "spruce", "ember", "vale", "breeze", "arbor", "sol", "cove", "aube", "bubbie",
	"haetsal", "hanul", "himari", "leher", "miko", "neer", "porto", "rindo", "rio", "rivage", "selva", "sonna", "tinta", "viola"}

// The built-in voices of the public GPT-Live API, marin its default.
var liveVoices = []string{"alloy", "ash", "ballad", "beacon", "bossa", "brise", "cedar", "cinder", "coral", "delta", "echo",
	"flitz", "gleam", "harema", "juni", "marin", "meridian", "nira", "noeul", "nuri", "quartz", "ripple", "sage", "shida",
	"shimmer", "sillage", "stone", "tempo", "verse", "vesper", "willow"}

// catalog lists models and voices across the providers; the services' own lists are kept 10 minutes.
type catalog struct {
	cfg *Config
	now func() time.Time

	mu    sync.Mutex
	cache map[string]cached
}

type cached struct {
	at    time.Time
	value any
}

const catalogTTL = 10 * time.Minute

func (c *catalog) get(key string, load func() (any, error)) (any, error) {
	c.mu.Lock()
	hit, ok := c.cache[key]
	c.mu.Unlock()
	if ok && c.now().Sub(hit.at) < catalogTTL {
		return hit.value, nil
	}
	v, err := load()
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	if c.cache == nil {
		c.cache = map[string]cached{}
	}
	c.cache[key] = cached{at: c.now(), value: v}
	c.mu.Unlock()
	return v, nil
}

func (c *catalog) provider(id string) *Provider {
	for i := range c.cfg.Providers {
		if c.cfg.Providers[i].ID == id {
			return &c.cfg.Providers[i]
		}
	}
	return nil
}

type providerModels struct {
	models []ModelChoice // ids without the provider prefix
	def    string
}

// providerModels: the configured models, else the service's own list (live dialect), else the dialect's default.
func (c *catalog) providerModels(ctx context.Context, p *Provider) (providerModels, error) {
	if len(p.Models) > 0 || p.Dialect == DialectCodex || p.Dialect == DialectRealtime || p.Dialect == DialectFake {
		ids := p.Models
		if len(ids) == 0 {
			if p.Dialect == DialectRealtime {
				ids = []string{"gpt-realtime", "gpt-realtime-mini"}
			} else {
				ids = []string{p.defaultModel()}
			}
		}
		out := providerModels{def: p.defaultModel()}
		for _, id := range ids {
			out.models = append(out.models, ModelChoice{ID: id})
		}
		return out, nil
	}
	v, err := c.get("models:"+p.ID, func() (any, error) {
		var res struct {
			Models []struct {
				ID          string `json:"id"`
				Name        string `json:"name"`
				DisplayName string `json:"displayName"`
				DisplaySnek string `json:"display_name"`
			} `json:"models"`
		}
		if err := getJSON(ctx, p, "/models", nil, &res); err != nil {
			return nil, err
		}
		out := providerModels{def: p.defaultModel()}
		for _, m := range res.Models {
			if !modelPattern.MatchString(m.ID) {
				continue
			}
			name := firstNonEmpty(m.Name, m.DisplayName, m.DisplaySnek)
			if name == m.ID {
				name = ""
			}
			out.models = append(out.models, ModelChoice{ID: m.ID, Name: name})
		}
		if out.def == "" && len(out.models) > 0 {
			out.def = out.models[0].ID
		}
		return out, nil
	})
	if err != nil {
		return providerModels{}, err
	}
	return v.(providerModels), nil
}

// models is GET /models: every provider's models as "<provider>/<model>". A provider whose list cannot be read is
// left out (and logged) rather than failing the others.
func (c *catalog) models(ctx context.Context, logf func(string, ...any)) ([]ModelChoice, string, error) {
	out := []ModelChoice{} // none while no upstream is set up: an empty list, not null
	var firstErr error
	def := ""
	several := len(c.cfg.Providers) > 1
	for i := range c.cfg.Providers {
		p := &c.cfg.Providers[i]
		pm, err := c.providerModels(ctx, p)
		if err != nil {
			logf("models: %s: %v", p.ID, err)
			if firstErr == nil {
				firstErr = fmt.Errorf("%s: %w", p.name(), err)
			}
			continue
		}
		for _, m := range pm.models {
			name := firstNonEmpty(m.Name, m.ID)
			if several {
				name += " · " + p.name()
			}
			out = append(out, ModelChoice{ID: p.ID + "/" + m.ID, Name: name})
		}
		if def == "" && pm.def != "" {
			def = p.ID + "/" + pm.def
		}
	}
	if len(out) == 0 && firstErr != nil {
		return nil, "", firstErr
	}
	if c.cfg.Default != "" {
		def = c.cfg.Default
	}
	return out, def, nil
}

// resolve turns the app's model ("<provider>/<model>", or empty for the default) into a provider and its model.
func (c *catalog) resolve(ctx context.Context, id string) (*Provider, string, error) {
	if len(c.cfg.Providers) == 0 {
		return nil, "", errNoUpstream
	}
	if id == "" {
		id = c.cfg.Default
	}
	if id == "" {
		p := &c.cfg.Providers[0]
		pm, err := c.providerModels(ctx, p)
		if err != nil {
			return nil, "", err
		}
		if pm.def == "" {
			return nil, "", fmt.Errorf("%s: no model to use (set default_model)", p.name())
		}
		return p, pm.def, nil
	}
	pid, model, ok := splitModel(id)
	if !ok {
		return nil, "", fmt.Errorf("model %q is not <provider>/<model>", id)
	}
	p := c.provider(pid)
	if p == nil {
		return nil, "", fmt.Errorf("%w: %s", errNoProvider, pid)
	}
	return p, model, nil
}

// voices is GET /voices?model=: the service's list (live dialect, GET <url>/voices), else the dialect's built-in one.
func (c *catalog) voices(ctx context.Context, id string) (voiceList, error) {
	p, model, err := c.resolve(ctx, id)
	if err != nil {
		return voiceList{}, err
	}
	builtin := func(ids []string, def string) voiceList {
		out := voiceList{Default: def}
		for _, v := range ids {
			out.Voices = append(out.Voices, VoiceChoice{ID: v, Name: strings.ToUpper(v[:1]) + v[1:]})
		}
		return out
	}
	switch p.Dialect {
	case DialectCodex:
		return builtin(codexVoices, "cove"), nil
	case DialectRealtime:
		return builtin(liveVoices, "marin"), nil
	case DialectFake:
		return voiceList{Voices: []VoiceChoice{}}, nil // it speaks no audio
	}
	v, err := c.get("voices:"+p.ID+"/"+model, func() (any, error) {
		var res struct {
			Voices       []VoiceChoice `json:"voices"`
			DefaultVoice string        `json:"defaultVoice"`
			Default      string        `json:"default_voice"`
		}
		if err := getJSON(ctx, p, "/voices", url.Values{"model": {model}}, &res); err != nil {
			return nil, err
		}
		out := voiceList{Default: firstNonEmpty(res.DefaultVoice, res.Default)}
		for _, v := range res.Voices {
			if voicePattern.MatchString(v.ID) {
				out.Voices = append(out.Voices, v)
			}
		}
		return out, nil
	})
	if err != nil {
		// A service without a voices list: the public API's built-in voices.
		return builtin(liveVoices, "marin"), nil
	}
	return v.(voiceList), nil
}

// getJSON GETs <the provider's url>/<path> over HTTPS with its credential.
func getJSON(ctx context.Context, p *Provider, path string, q url.Values, out any) error {
	u, err := url.Parse(p.URL)
	if err != nil {
		return err
	}
	u.Scheme = strings.Replace(strings.Replace(u.Scheme, "wss", "https", 1), "ws", "http", 1)
	u.Path = strings.TrimRight(u.Path, "/") + path
	u.RawQuery = q.Encode()
	key, err := p.credential(ctx)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, "GET", u.String(), nil)
	if err != nil {
		return err
	}
	if key != "" {
		req.Header.Set("Authorization", "Bearer "+key)
	}
	res, err := p.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(res.Body, 4<<20))
	if res.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d %s", u.Path, res.StatusCode, clipRunes(string(raw), 200))
	}
	return json.Unmarshal(raw, out)
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}
