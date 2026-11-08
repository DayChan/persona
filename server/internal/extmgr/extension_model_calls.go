package extmgr

import (
	"context"
	"encoding/json"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Model calls by extensions (#6102, docs/extensions/extensions.md "Model calls"): an extension the user granted the models
// scope calls model.complete; it goes to the assistant, which holds the keys, with the extension's id (which the
// extension cannot set), and runs on the extension's purpose extension.<id> in Settings › Models. The legacy
// full-hub grant does not include it. No budget and no cap (the user's decision).

// extensionModelCall is model.complete from extension id.
func (ps *extensions) extensionModelCall(ctx context.Context, id string, params json.RawMessage) (any, error) {
	if ps.reg == nil || ps.reg.GetExact(id) == nil || !ps.reg.HubScopeGranted(id, extension.ScopeModels) {
		data, _ := json.Marshal(map[string]string{"scope": extension.ScopeModels})
		return nil, &rpc.Error{Code: extension.CodePermissionDenied, Message: assistantwire.MethodModelComplete + ": requires " + extension.ScopeModels, Data: data}
	}
	if ps.h == nil {
		return nil, &rpc.Error{Code: assistantwire.CodeAssistantOffline, Message: errAssistantOffline.Error()}
	}
	p := ps.reg.SnapshotExact(id)
	storageID, err := ps.assistantStorageID(ctx, p)
	if err != nil {
		return nil, err
	}
	var out json.RawMessage
	if err := ps.h.kbAsk(ctx, assistantwire.MethodModelsExtensionComplete, assistantwire.ExtensionComplete{Extension: id, StorageID: storageID, Params: params}, &out); err != nil {
		return nil, err
	}
	return out, nil
}

// assistantStorageID checks support on the authenticated manager-to-assistant channel.
// Only a registered installation may supply the storage owner; extension params remain opaque.
func (ps *extensions) assistantStorageID(ctx context.Context, p *extension.Extension) (string, error) {
	if p == nil {
		return "", i18n.Err("extension-identity.invalid")
	}
	if p.Record.StorageKey() == p.Record.ID {
		return "", nil
	}
	if ps.h == nil {
		return "", i18n.Err("extension-identity.assistant_required")
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var capability struct {
		Supported bool `json:"supported"`
	}
	if err := ps.h.kbAsk(ctx, assistantwire.MethodExtensionIdentity, map[string]any{}, &capability); err != nil || !capability.Supported {
		return "", i18n.Err("extension-identity.assistant_required")
	}
	return p.Record.StorageKey(), nil
}

// modelExtensions are the extensions switched on and granted models: each has its own purpose in Settings › Models
// (plugins.models).
func (ps *extensions) modelExtensions() []assistantwire.ModelExtension {
	out := []assistantwire.ModelExtension{}
	if ps == nil || ps.reg == nil {
		return out
	}
	for _, p := range ps.reg.List() {
		if p.Full == nil || !ps.reg.HubScopeGranted(p.Record.ID, extension.ScopeModels) {
			continue
		}
		storageID, err := ps.assistantStorageID(context.Background(), p)
		if err != nil {
			continue
		} // An old assistant must not invent a new model-purpose owner.
		out = append(out, assistantwire.ModelExtension{ID: p.Record.ID, Name: p.Full.Name, StorageID: storageID})
	}
	return out
}
