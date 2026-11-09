// Package aiwire is the model configuration as the contract carries it (docs/server/architecture.md "Contract packages"):
// the manager's model settings travel over the bus in it. Types and pure helpers only; internal/ai, which calls the
// models, aliases it.
package aiwire

// Config is a model to call: the gateway, its API, the key and the model (manager.json's "ai" and the per-task models).
type Config struct {
	// API is "messages" (Anthropic Messages, the default) "responses" (OpenAI Responses), or "chat" (OpenAI Chat Completions).
	API        string `json:"api"`
	BaseURL    string `json:"base_url"`
	APIKeyFile string `json:"api_key_file"`
	// APIKey is an in-memory credential supplied by the hub's encrypted provider store.
	APIKey string `json:"-"`
	Model  string `json:"model"`
	// ReasoningEffort is sent as reasoning.effort to the Responses API when set (e.g. "low").
	ReasoningEffort string `json:"reasoning_effort"`
	// Bearer sends the key to the Messages API as Authorization: Bearer instead of x-api-key, for gateways that take
	// only that (what Claude Code does with ANTHROPIC_AUTH_TOKEN). Responses and Chat Completions always use Bearer.
	Bearer bool `json:"bearer,omitempty"`
}

// Over is c with its empty fields taken from base: a task that uses another model names only what differs.
func (c Config) Over(base Config) Config {
	if c.API == "" {
		c.API = base.API
	}
	if c.BaseURL == "" { // the gateway, and how it takes the key
		c.BaseURL, c.Bearer = base.BaseURL, base.Bearer
	}
	if c.APIKeyFile == "" {
		c.APIKeyFile = base.APIKeyFile
	}
	if c.Model == "" {
		c.Model = base.Model
	}
	if c.ReasoningEffort == "" {
		c.ReasoningEffort = base.ReasoningEffort
	}
	return c
}

// Configured says whether there is a model to call: an address, a model and a key (or a key file). Nothing is
// assumed (it used to default to one person's gateway and key file): without a model set up, the helpers that need
// one say so.
func (c Config) Configured() bool {
	return c.BaseURL != "" && c.Model != "" && (c.APIKeyFile != "" || c.APIKey != "")
}
