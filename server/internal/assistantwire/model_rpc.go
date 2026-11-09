package assistantwire

import "encoding/json"

// Model calls by extensions (#6102, docs/extensions/extensions.md "Model calls"): an extension granted the models scope calls
// model.complete; the manager hands it to the assistant with the extension's id (ExtensionComplete), and the
// assistant answers it on the extension's own purpose extension.<id> in Settings › Models (inheriting "Knowledge
// base"'s until the user picks another). The extension never sees a key. No budget and no cap (the user's decision):
// what a call spent comes back in its usage.
const (
	// MethodExtensionIdentity probes the authenticated assistant's stable extension ownership support.
	MethodExtensionIdentity = "extensions.identity"
	// MethodModelComplete is the extension-facing method (schema/extension/models.schema.json).
	MethodModelComplete = "model.complete"
	// MethodModelsExtensionComplete is how the manager hands the assistant an extension's model.complete:
	// ExtensionComplete → ModelCompleteResult. Not on the extensions' allowlist.
	MethodModelsExtensionComplete = "models.extension_complete"
	// PluginsModels: {} → {extensions: [ModelExtension]}, the extensions switched on that are granted the models
	// scope: each has a purpose extension.<id> in Settings › Models.
	PluginsModels = "plugins.models"
)

// Error codes of model.complete besides permission_denied, invalid_params and assistant_offline.
const (
	// CodeModelUnavailable: the extension's purpose has nothing it can call (no connection with an address, a model
	// and a key); the user sets one up in Settings › Models.
	CodeModelUnavailable = "model_unavailable"
	// CodeModelPaused: the connection is held (quota used up or busy, ai.IsPaused); data ModelPause. Nothing was
	// sent: try again after until.
	CodeModelPaused = "model_paused"
	// CodeModelFailed: the upstream failed the request (its message says why): an error status, a dropped
	// connection. A declined request is CodeModelRefused instead.
	CodeModelFailed = "model_failed"
	// CodeModelRefused: the model declined to answer this input (a refusal, a content filter); data ModelRefusal.
	// Sending it again unchanged gets the same answer: split it, drop a part or report it instead.
	CodeModelRefused = "model_refused"
	// CodeInvalidOutput: with json_schema, the reply was still not JSON matching it after one more try; data
	// ModelInvalidOutput.
	CodeInvalidOutput = "invalid_output"
	// CodeModelTimeout: the call ran past its timeout_seconds.
	CodeModelTimeout = "timeout"
)

// ExtensionPurposePrefix and an extension's id is its model purpose.
const ExtensionPurposePrefix = "extension."

// ModelExtension is an extension with a model purpose of its own.
type ModelExtension struct {
	StorageID string `json:"storage_id,omitempty"`
	ID        string `json:"id"`
	Name      string `json:"name"`
}

// ExtensionComplete is an extension's model.complete params as it sent them, and who it is.
type ExtensionComplete struct {
	StorageID string          `json:"storage_id,omitempty"`
	Extension string          `json:"extension"`
	Params    json.RawMessage `json:"params"`
}

func (x ModelExtension) StorageKey() string {
	if x.StorageID != "" {
		return x.StorageID
	}
	return x.ID
}

func (x ExtensionComplete) StorageKey() string {
	if x.StorageID != "" {
		return x.StorageID
	}
	return x.Extension
}

// ModelMessage is one turn: the user's (role user) or the model's earlier answer (role assistant).
type ModelMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ModelCompleteParams is model.complete's params.
type ModelCompleteParams struct {
	System   string         `json:"system,omitempty"`
	Messages []ModelMessage `json:"messages"`
	// MaxTokens is the room for the answer (default 4096, at most 64000).
	MaxTokens int `json:"max_tokens,omitempty"`
	// JSONSchema asks for a JSON value matching this schema: the result's json has it.
	JSONSchema json.RawMessage `json:"json_schema,omitempty"`
	// TimeoutSeconds bounds the whole call, a second try for json_schema included (default 120, at most 600).
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`
	// Label says what the call is for ("daily", "weekly"): it goes in the hub's log line with the usage.
	Label string `json:"label,omitempty"`
}

// ModelUsage is what a call spent, every request of it together (a second try for json_schema included).
type ModelUsage struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	Requests                 int `json:"requests"`
}

// ModelCompleteResult is model.complete's result.
type ModelCompleteResult struct {
	Text string `json:"text"`
	// JSON is the reply decoded, with json_schema only.
	JSON json.RawMessage `json:"json,omitempty"`
	// Model is the model that answered (as the upstream says).
	Model string `json:"model"`
	// StopReason is end_turn, or max_tokens when the answer was cut off.
	StopReason string     `json:"stop_reason"`
	Usage      ModelUsage `json:"usage"`
}

// ModelPause is the data of model_paused.
type ModelPause struct {
	Reason string `json:"reason"` // quota | busy
	Until  string `json:"until"`  // RFC 3339
}

// ModelInvalidOutput is the data of invalid_output: why the last reply did not match, and what was spent.
type ModelInvalidOutput struct {
	Problem string     `json:"problem"`
	Text    string     `json:"text"`
	Usage   ModelUsage `json:"usage"`
}

// ModelRefusal is the data of model_refused: what the upstream said and what the declined call spent.
type ModelRefusal struct {
	// StopReason is the upstream's reason: refusal or content_filter.
	StopReason string     `json:"stop_reason,omitempty"`
	Usage      ModelUsage `json:"usage"`
}
