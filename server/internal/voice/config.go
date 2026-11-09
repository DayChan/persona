// Package voice is the built-in voice plugin (`personad voice`, docs/extensions/extensions.md "Built-in voice extension"): full-duplex voice
// conversations in the app over a realtime service that speaks the GPT-Live wire. The hub starts it like any plugin
// and relays /api/v1/plugins/<id>/… to the HTTP server it serves on PERSONA_PLUGIN_SOCKET.
//
// One plugin can have several upstreams (providers): each is a /v1/live endpoint, its dialect, its credential. The
// models of all of them make one list, and the model the app picks decides which upstream a conversation goes to.
package voice

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/nwverse/persona/server/internal/identity"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Dialects of the GPT-Live wire.
const (
	// DialectLive: the public GPT-Live API (session.start, session.input_audio.append, session.delegation.created +
	// response.event{function_call}, session.commentary.append), also served by deployments that speak it for their
	// own models. Function tools are declared in the session.
	DialectLive = "live"
	// DialectCodex: what Codex speaks to gpt-live-1-codex (codex-rs "frameless bidi", OpenAI-Alpha: quicksilver=v2):
	// session.update, input_audio.append, delegation.created{item} and delegation.context.append. No function tools:
	// the model hands work over as delegations.
	DialectCodex = "codex"
	// DialectRealtime: the OpenAI Realtime v2 WebSocket wire (/v1/realtime, gpt-realtime / gpt-realtime-mini):
	// session.update, input_audio_buffer.append, response.audio.delta / response.output_audio.delta,
	// conversation.item.create and response.create.
	DialectRealtime = "realtime"
	// DialectFake: no service at all, for development (fake.go; hub.json only, never the settings form): what the app
	// types is handed over as it is, and what the model would be told comes back as its words.
	DialectFake = "fake"
)

// Config is the plugin's configuration: the upstreams of the hub.json entry the hub took over and of the settings
// form, merged (Merge, docs/assistant/voice.md "Configurable GPT-Live upstream"); a hand-run plugin's is given whole (the old format).
type Config struct {
	Providers []Provider `json:"providers"`
	// Default is the model a conversation uses when the app names none ("<provider>/<model>"); empty: the first
	// provider's default.
	Default string `json:"default,omitempty"`
	// Owner is how the voice model calls the user; Notes is appended to its instructions.
	Owner string `json:"owner,omitempty"`
	Notes string `json:"notes,omitempty"`

	// Setup is what is missing before a conversation can start when no upstream is usable (SetupURLAndKey, SetupKey,
	// SetupURL); "" when one is. The plugin runs either way.
	Setup string `json:"-"`
	// Warnings are settings that were not taken (WarnFormIDTaken, WarnFormNoKey, WarnDefaultUnknown).
	Warnings []string `json:"-"`
}

// What a configuration without a usable upstream lacks (Config.Setup).
const (
	SetupURLAndKey = "url_and_key" // the form has neither the service's address nor its key
	SetupKey       = "key"         // the form has the address, not its key
	SetupURL       = "url"         // the form has a key but no address
)

// Settings that were not taken (Config.Warnings).
const (
	WarnFormIDTaken    = "form_id_taken"   // hub.json has an upstream of id custom: the form's is left out
	WarnFormNoKey      = "form_no_key"     // the form has an address without its key while hub.json's upstreams serve
	WarnDefaultUnknown = "default_unknown" // hub.json's default names no upstream: the first upstream's default instead
)

// FormProvider is the id of the settings form's upstream: its models are "custom/<model>".
const FormProvider = "custom"

// GatewayProvider is the id of the upstream derived from a quota gateway extension's settings when configured with an API key.
const GatewayProvider = "gateway"

// Form is the built-in extension's settings form (its manifest's configuration, extmgr/voice_extension.go): one upstream
// and how the model talks; the upstream's key is the extension's secret "key".
type Form struct {
	URL          string   `json:"url"`
	Name         string   `json:"name"`
	Dialect      string   `json:"dialect"`
	Models       []string `json:"models"`
	DefaultModel string   `json:"default_model"`
	InputRate    int      `json:"input_rate"`
	Owner        string   `json:"owner"`
	Notes        string   `json:"notes"`
}

// Sources are where the built-in extension's settings come from (docs/assistant/voice.md "How the extension gets its settings").
type Sources struct {
	// Form is the settings form (PERSONA_EXTENSION_CONFIG), Key its secret key (PERSONA_EXTENSION_SECRETS).
	Form json.RawMessage
	Key  string
	// HubJSON is the config of the hub.json entry the hub took over (PERSONA_VOICE_HUB_CONFIG): providers, default,
	// owner, notes.
	HubJSON json.RawMessage
	// Gateway is an upstream provider derived from a quota gateway extension's settings
	// (PERSONA_VOICE_GATEWAY_CONFIG) when the extension has a gateway_url and an API key.
	Gateway json.RawMessage
}

// Merge makes one configuration of the sources: hub.json's upstreams first, then the form's (id custom) once it has
// an address and a key; the form's owner and notes over hub.json's. A form holding providers is the old format (a
// plugin run by hand, or hub.json's config given as the plugin's own): taken whole. No usable upstream is not an
// error: Setup says what is missing.
func Merge(s Sources) (Config, error) {
	var c Config
	form := bytes.TrimSpace(s.Form)
	if len(form) > 0 {
		var keys map[string]json.RawMessage
		if err := json.Unmarshal(form, &keys); err != nil {
			return Config{}, fmt.Errorf("config: %w", err)
		}
		if _, old := keys["providers"]; old {
			if err := json.Unmarshal(form, &c); err != nil {
				return Config{}, fmt.Errorf("config: %w", err)
			}
			return c, c.validate()
		}
	}
	if raw := bytes.TrimSpace(s.HubJSON); len(raw) > 0 {
		if err := json.Unmarshal(raw, &c); err != nil {
			return Config{}, fmt.Errorf("hub.json config: %w", err)
		}
		if err := c.validateProviders(); err != nil {
			return Config{}, fmt.Errorf("hub.json %w", err)
		}
	}
	var f Form
	if len(form) > 0 {
		if err := json.Unmarshal(form, &f); err != nil {
			return Config{}, fmt.Errorf("config: %w", err)
		}
	}
	c.Owner, c.Notes = firstNonEmpty(f.Owner, c.Owner), firstNonEmpty(f.Notes, c.Notes)
	fromHubJSON := len(c.Providers) > 0
	switch {
	case f.URL == "":
	case slices.ContainsFunc(c.Providers, func(p Provider) bool { return p.ID == FormProvider }):
		c.Warnings = append(c.Warnings, WarnFormIDTaken)
	case s.Key == "":
		if fromHubJSON {
			c.Warnings = append(c.Warnings, WarnFormNoKey)
		}
	default:
		p := Provider{ID: FormProvider, Name: f.Name, URL: f.URL, Dialect: f.Dialect, Key: s.Key, Models: f.Models,
			DefaultModel: f.DefaultModel, InputRate: f.InputRate}
		if err := p.validate(); err != nil {
			return Config{}, fmt.Errorf("settings form: %w", err)
		}
		c.Providers = append(c.Providers, p)
	}
	if raw := bytes.TrimSpace(s.Gateway); len(raw) > 0 {
		var gw Provider
		if err := json.Unmarshal(raw, &gw); err != nil {
			return Config{}, fmt.Errorf("gateway config: %w", err)
		}
		if err := gw.validate(); err != nil {
			return Config{}, fmt.Errorf("gateway %w", err)
		}
		if !slices.ContainsFunc(c.Providers, func(p Provider) bool { return p.ID == gw.ID }) {
			c.Providers = append(c.Providers, gw)
		}
	}
	if c.Default != "" {
		if id, _, ok := splitModel(c.Default); !ok || !slices.ContainsFunc(c.Providers, func(p Provider) bool { return p.ID == id }) {
			c.Default = ""
			if len(c.Providers) > 0 {
				c.Warnings = append(c.Warnings, WarnDefaultUnknown)
			}
		}
	}
	if len(c.Providers) == 0 {
		switch {
		case f.URL == "" && s.Key != "":
			c.Setup = SetupURL
		case f.URL != "":
			c.Setup = SetupKey
		default:
			c.Setup = SetupURLAndKey
		}
	}
	return c, nil
}

// Provider is one upstream realtime service.
type Provider struct {
	ID      string `json:"id"`             // a-z0-9_-, the prefix of its model ids
	Name    string `json:"name,omitempty"` // shown after the model's name when there are several providers
	URL     string `json:"url"`            // the session socket: wss://<host>/v1/live
	Dialect string `json:"dialect,omitempty"`
	// The credential, sent as Authorization: Bearer: Key itself, the first line of KeyFile, or what KeyCommand (sh -c)
	// prints. A command is run again for every conversation, so a credential renewed elsewhere is picked up.
	Key        string `json:"key,omitempty"`
	KeyFile    string `json:"key_file,omitempty"`
	KeyCommand string `json:"key_command,omitempty"`
	// SSH dials the service through that machine (`ssh -W host:port <ssh>`): for a hub machine that cannot reach it.
	// TLS still runs end to end from here.
	SSH string `json:"ssh,omitempty"`
	// Models offered; empty lists the service's own (live: GET <url>/models) or the dialect's default.
	Models       []string `json:"models,omitempty"`
	DefaultModel string   `json:"default_model,omitempty"`
	// Tools (live dialect): declare Persona's function tools in session.start. Some services hand work over only
	// through a declared function (docs/lessons/voice.md "Other behavior"); the public GPT-Live API refuses
	// session.tools, and without them the model's client delegations go to the Persona main conversation, as with the
	// codex dialect. Unset (the settings form's upstream, #1420) is auto: declared, and a service that refuses them as
	// it starts the session gets the session again without them, which the plugin then remembers for that provider.
	// true or false (hub.json) decides it, with no fallback.
	Tools *bool `json:"tools,omitempty"`
	// InputRate is the microphone's sample rate (16000 or 24000); 0 is the dialect's (live 16000, codex 24000).
	InputRate int `json:"input_rate,omitempty"`
}

var (
	providerIDPattern = lazyre.New(`^[a-z0-9][a-z0-9_-]{0,31}$`)
	modelPattern      = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
	voicePattern      = lazyre.New(`^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$`)
)

// LoadConfig reads the settings the hub hands the plugin (docs/assistant/voice.md "How the extension gets its settings"): the settings form in
// PERSONA_EXTENSION_CONFIG, its key in PERSONA_EXTENSION_SECRETS, the config of the hub.json entry the hub took over
// in PERSONA_VOICE_HUB_CONFIG. The former PERSONA_PLUGIN_* names are read too.
func LoadConfig() (Config, error) {
	var secrets map[string]string
	if raw := strings.TrimSpace(extensionEnv("SECRETS")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &secrets); err != nil {
			return Config{}, errors.New("PERSONA_EXTENSION_SECRETS is not a JSON object of strings")
		}
	}
	return Merge(Sources{Form: json.RawMessage(extensionEnv("CONFIG")), Key: secrets["key"],
		HubJSON: json.RawMessage(identity.Getenv("PERSONA_VOICE_HUB_CONFIG")),
		Gateway: json.RawMessage(identity.Getenv("PERSONA_VOICE_GATEWAY_CONFIG"))})
}

// isLoopbackHost reports whether h is a loopback host on this machine (127.0.0.1, localhost or ::1).
func isLoopbackHost(h string) bool {
	switch strings.ToLower(strings.TrimSuffix(strings.Trim(strings.TrimSpace(h), "[]"), ".")) {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}

// GatewayLiveURL converts a quota gateway_url (https:// or loopback http://127.0.0.1 / localhost / [::1])
// into its /v1/live WebSocket URL. Returns "", false for invalid or non-loopback http:// URLs.
func GatewayLiveURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", false
	}
	var scheme string
	switch strings.ToLower(u.Scheme) {
	case "https":
		scheme = "wss"
	case "http":
		if !isLoopbackHost(u.Hostname()) {
			return "", false
		}
		scheme = "ws"
	default:
		return "", false
	}
	basePath := strings.TrimRight(u.Path, "/")
	for _, suffix := range []string{"/openai/v1", "/backend-api/codex", "/v1"} {
		if strings.HasSuffix(basePath, suffix) {
			basePath = strings.TrimSuffix(basePath, suffix)
			break
		}
	}
	return scheme + "://" + u.Host + basePath + "/v1/live", true
}

// GatewayProviderFromConfig builds a voice Provider from a quota gateway extension's configuration and gateway_key when
// gateway_url is valid and key_kind is "api" (management keys cannot authenticate model/realtime routes).
func GatewayProviderFromConfig(config json.RawMessage, name, key string) (Provider, bool) {
	key = strings.TrimSpace(key)
	if key == "" {
		return Provider{}, false
	}
	var c struct {
		URL  string `json:"gateway_url"`
		Kind string `json:"key_kind"`
	}
	if err := json.Unmarshal(config, &c); err != nil || c.Kind != "api" {
		return Provider{}, false
	}
	liveURL, ok := GatewayLiveURL(c.URL)
	if !ok {
		return Provider{}, false
	}
	if name = strings.TrimSpace(name); name == "" {
		name = "Gateway"
	}
	return Provider{
		ID:           GatewayProvider,
		Name:         name,
		URL:          liveURL,
		Dialect:      DialectCodex,
		Key:          key,
		Models:       []string{"gpt-live-1-codex"},
		DefaultModel: "gpt-live-1-codex",
		InputRate:    24000,
	}, true
}

// extensionEnv is PERSONA_EXTENSION_<name>, else its former PERSONA_PLUGIN_<name> name.
func extensionEnv(name string) string {
	if v, ok := identity.LookupEnv("PERSONA_EXTENSION_" + name); ok {
		return v
	}
	return identity.Getenv("PERSONA_PLUGIN_" + name)
}

// validate holds a whole configuration (the old format) to having an upstream, each of them sound.
func (c *Config) validate() error {
	if len(c.Providers) == 0 {
		return errors.New("config: no providers")
	}
	return c.validateProviders()
}

// validateProviders checks each upstream and that no two share an id.
func (c *Config) validateProviders() error {
	seen := map[string]bool{}
	for i := range c.Providers {
		p := &c.Providers[i]
		if err := p.validate(); err != nil {
			return err
		}
		if seen[p.ID] {
			return fmt.Errorf("config: provider %s twice", p.ID)
		}
		seen[p.ID] = true
	}
	return nil
}

// validate checks one upstream and fills in its dialect.
func (p *Provider) validate() error {
	if !providerIDPattern.MatchString(p.ID) {
		return fmt.Errorf("config: provider id %q: a-z0-9_-, at most 32", p.ID)
	}
	if p.Dialect == "" {
		if u, err := url.Parse(p.URL); err == nil && strings.HasSuffix(strings.TrimRight(u.Path, "/"), "/realtime") {
			p.Dialect = DialectRealtime
		} else {
			p.Dialect = DialectLive
		}
	}
	if p.Dialect != DialectLive && p.Dialect != DialectCodex && p.Dialect != DialectRealtime && p.Dialect != DialectFake {
		return fmt.Errorf("config: provider %s: dialect %q (live, codex, realtime or fake)", p.ID, p.Dialect)
	}
	if p.Dialect == DialectFake {
		if p.URL != "" || p.SSH != "" {
			return fmt.Errorf("config: provider %s: the fake dialect connects nowhere (no url, no ssh)", p.ID)
		}
	} else if !strings.HasPrefix(p.URL, "wss://") && !strings.HasPrefix(p.URL, "ws://") {
		return fmt.Errorf("config: provider %s: url must be ws:// or wss://", p.ID)
	}
	if p.InputRate != 0 && p.InputRate != 16000 && p.InputRate != 24000 {
		return fmt.Errorf("config: provider %s: input_rate is 16000 or 24000", p.ID)
	}
	for _, m := range append(slices.Clone(p.Models), p.DefaultModel) {
		if m != "" && !modelPattern.MatchString(m) {
			return fmt.Errorf("config: provider %s: model %q", p.ID, m)
		}
	}
	return nil
}

func (p *Provider) name() string {
	if p.Name != "" {
		return p.Name
	}
	return p.ID
}

// toolsMode is whether the provider's sessions declare Persona's function tools, and whether that is auto (Tools
// unset): only then does a session the service refuses them on start again without them (connect).
func (p *Provider) toolsMode() (declare, auto bool) {
	switch {
	case p.Dialect != DialectLive && p.Dialect != DialectRealtime:
		return false, false
	case p.Tools == nil:
		return true, true
	}
	return *p.Tools, false
}

func (p *Provider) inputRate() int {
	switch {
	case p.InputRate != 0:
		return p.InputRate
	case p.Dialect == DialectCodex || p.Dialect == DialectRealtime:
		return 24000
	default:
		return 16000
	}
}

// defaultModel is the provider's own default: configured, else the first configured, else the dialect's.
func (p *Provider) defaultModel() string {
	switch {
	case p.DefaultModel != "":
		return p.DefaultModel
	case len(p.Models) > 0:
		return p.Models[0]
	case p.Dialect == DialectCodex:
		return "gpt-live-1-codex"
	case p.Dialect == DialectRealtime:
		return "gpt-realtime"
	case p.Dialect == DialectFake:
		return "fake"
	}
	return ""
}

// splitModel parses "<provider>/<model>".
func splitModel(id string) (provider, model string, ok bool) {
	provider, model, ok = strings.Cut(id, "/")
	return provider, model, ok && providerIDPattern.MatchString(provider) && modelPattern.MatchString(model)
}

func expandHome(p string) string {
	if rest, ok := strings.CutPrefix(p, "~/"); ok {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, rest)
	}
	return p
}
