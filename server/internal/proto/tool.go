package proto

import "encoding/json"

// Tool is an MCP tool definition.
type Tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
	// Shell are the words that run a gated command's tool in a shell instead (its command, its skill's script): a
	// runtime refuses them in an assistant conversation's shell, where the gate and its credentials are not
	// (integrations/dsh), and points at the tool.
	Shell []string `json:"shell,omitempty"`
}

// Content is an MCP tool result content block.
type Content struct {
	Type     string `json:"type"` // text | image
	Text     string `json:"text,omitempty"`
	Data     string `json:"data,omitempty"`
	MimeType string `json:"mime_type,omitempty"`
}

type ToolResult struct {
	Content []Content `json:"content"`
	IsError bool      `json:"is_error"`
	// ErrorKind is what kind of failure an error is, for a program to act on (personad tool's exit code), when the
	// hub, the node or the connector knows: one of the ToolError* below; empty is a plain failure, or an older hub's.
	ErrorKind string `json:"error_kind,omitempty"`
	// RetryAfter is how many seconds a rate limit lasts (ErrorKind rate_limited), when it is known.
	RetryAfter int `json:"retry_after,omitempty"`
}

// The kinds of a tool's failure (ToolResult.ErrorKind), as personad tool's exit codes have them.
const (
	ToolErrorDeclined    = "declined"     // the user (or connector_gate) did not allow the write
	ToolErrorUnavailable = "unavailable"  // the node, the hub or the connector cannot be reached or is not signed in
	ToolErrorRateLimited = "rate_limited" // the connector waits out a rate limit (RetryAfter seconds)
)

func TextResult(text string, isErr bool) ToolResult {
	return ToolResult{Content: []Content{{Type: "text", Text: text}}, IsError: isErr}
}

// ErrorResult is a failure of a known kind (ToolError*), retryAfter seconds for a rate limit (0: not known).
func ErrorResult(kind, text string, retryAfter int) ToolResult {
	res := TextResult(text, true)
	res.ErrorKind, res.RetryAfter = kind, max(retryAfter, 0)
	return res
}
