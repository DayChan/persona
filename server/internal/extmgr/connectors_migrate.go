package extmgr

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/connector/proc"
)

// A plugin may take over one of the hub's own connectors while it moves out of the hub (connector.MigratableConnectors:
// Feishu through the Lark extension, docs/plans/plan-extensions.md "Step 3"). The id stays (the knowledge base's chats
// lark-<feed>, the inbox's source and mutes, Settings › Connectors all go by it): while the plugin's connector is
// registered the hub's own is out of the registry, and the hub's own loops for it stand down (h.migrated holds it);
// when the plugin is switched off or removed, the hub's comes back.

// yieldOwnConnector takes the hub's own connector id out of the registry for a plugin's (a migratable id only).
func (h *Manager) yieldOwnConnector(id string) {
	if !slices.Contains(connector.MigratableConnectors, id) {
		return
	}
	own := h.connectors.Get(id)
	if own == nil {
		return
	}
	if _, isExtension := own.(*proc.Conn); isExtension {
		return
	}
	h.migrated.Store(id, own)
	h.connectors.Remove(id)
	h.logf("connector %s: a plugin takes it over", id)
}

// restoreOwnConnector puts the hub's own connector id back (the plugin's is gone, or could not be added).
func (h *Manager) restoreOwnConnector(id string) {
	v, ok := h.migrated.LoadAndDelete(id)
	if !ok || h.connectors.Get(id) != nil {
		return
	}
	own := v.(connector.Connector)
	if r, ok := own.(interface{ Reload() }); ok {
		r.Reload() // the plugin's may have signed in or out under the same id
	}
	if err := h.addConnector(own); err != nil {
		h.logf("connector %s: back to the hub's own: %v", id, err)
		return
	}
	// The user's switches and settings as they are now (changed while the plugin's held the id), and GitHub's source.
	h.restoreSwitchesOf(own, h.connectorSettings(context.Background()))
	if id == "github" {
		h.restoreGitHubSource(context.Background())
	}
	h.logf("connector %s: back to the hub's own", id)
}

// removeExtensionConnector takes a plugin's connector out, and gives a migratable id back to the hub.
func (h *Manager) removeExtensionConnector(id string) {
	h.connectors.Remove(id)
	h.restoreOwnConnector(id)
}

// hasOwnConnector says whether the hub has a connector of its own with id (running, or set aside for a plugin's).
func (h *Manager) hasOwnConnector(id string) bool {
	if _, ok := h.migrated.Load(id); ok {
		return true
	}
	c := h.connectors.Get(id)
	if c == nil {
		return false
	}
	_, isExtension := c.(*proc.Conn)
	return !isExtension
}
