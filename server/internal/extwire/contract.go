package extwire

import "strings"

// The scopes of the hub's API an extension may ask for (permissions.hub; docs/extensions/extensions.md"Permissions"). Each route of the
// hub an extension's token may call names one (extmgr/extension_scopes.go); a route naming none is for full tokens
// only.
const (
	ScopeKBRead        = "kb.read"
	ScopeKBWrite       = "kb.write"
	ScopeInboxRead     = "inbox.read"
	ScopeInboxWrite    = "inbox.write"
	ScopeAgentsRead    = "agents.read"
	ScopeAgentsControl = "agents.control"
	ScopeMachinesRead  = "machines.read"
	ScopeNotify        = "notify"
	ScopeAssistantAsk  = "assistant.ask"
	ScopeTodosRead     = "todos.read"
	ScopeTodosWrite    = "todos.write"
	// ScopeAgentsPermissionMode: changing the permission mode of a conversation the extension opened itself, and of no
	// other (the hub checks who opened it: hub/extension_modes.go).
	ScopeAgentsPermissionMode = "agents.permission_mode"
	// ScopeModels: calling a model through the hub (model.complete) on the extension's own purpose in Settings ›
	// Models; never a key (#6102).
	ScopeModels = "models"
	// ScopeAssistantRead: reading the assistant's own-space conversations, the user's and the assistant's words
	// (assistant.conversations, assistant.messages; #1288). assistant.ask does not include it.
	ScopeAssistantRead = "assistant.read"
	// ScopeCallPrefix and an extension's id: calling that extension's own HTTP (/api/v1/extensions/<id>/…).
	ScopeCallPrefix = "extensions.call:"
)

// ProviderModel is one model of a provider.
type ProviderModel struct {
	ID   string `json:"id"`
	Name string `json:"name,omitempty"`
}

// APIReceipt is one route of a plugin's API with a send receipt: Method and Path (under the prefix, "*" one segment)
// and the receipt's Kind. The receipt's id comes from the body's client_id, or the caller's X-Persona-Op-Id.
type APIReceipt struct {
	Method string `json:"method"`
	Path   string `json:"path"`
	Kind   string `json:"kind"`
}

// Matches says whether a request (its path under the prefix) is the receipt's route.
func (r APIReceipt) Matches(method, path string) bool {
	if !strings.EqualFold(r.Method, method) {
		return false
	}
	want := strings.Split(strings.Trim(r.Path, "/"), "/")
	got := strings.Split(strings.Trim(path, "/"), "/")
	if len(want) != len(got) {
		return false
	}
	for i := range want {
		if want[i] != "*" && want[i] != got[i] {
			return false
		}
	}
	return true
}
