package extmgr

import (
	"context"
	"encoding/json"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// extensionAssistantCall is assistant.conversations / assistant.messages from extension id (#1288): the user's
// conversations with the assistant, read-only, for an extension granted assistant.read (checked against the grant,
// as kb.read is; the legacy full-hub grant covers it). The manager owns the conversations and answers them.
func (ps *extensions) extensionAssistantCall(ctx context.Context, id, method string, params json.RawMessage) (any, error) {
	switch method {
	case assistantwire.MethodAssistantConversations, assistantwire.MethodAssistantMessages:
	default:
		return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
	}
	if ps.reg == nil || !ps.reg.HubScopeAllowed(id, extension.ScopeAssistantRead) {
		data, _ := json.Marshal(map[string]string{"scope": extension.ScopeAssistantRead})
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: method + ": requires " + extension.ScopeAssistantRead, Data: data}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	var out json.RawMessage
	if err := ps.h.kbAsk(ctx, method, params, &out); err != nil { // kbAsk: the manager's answer, assistant_offline coded
		return nil, err
	}
	return out, nil
}
